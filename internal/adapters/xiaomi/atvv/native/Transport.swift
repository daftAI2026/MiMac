import CoreBluetooth
import Foundation

// MiMacATVVTransportCreate starts an already-connected-device GATT transport.
// It never scans, pairs, or opens the microphone. Callback byte pointers are
// valid only for the duration of that callback.
public typealias MiMacATVVEventCallback = @convention(c) (
    UInt64, Int32, Int32, UnsafePointer<UInt8>?, Int32
) -> Void

private let atvvServiceUUID = CBUUID(string: "AB5E0001-5A21-4F05-BC7D-AF01F617B664")
private let atvvTransmitUUID = CBUUID(string: "AB5E0002-5A21-4F05-BC7D-AF01F617B664")
private let atvvAudioUUID = CBUUID(string: "AB5E0003-5A21-4F05-BC7D-AF01F617B664")
private let atvvControlUUID = CBUUID(string: "AB5E0004-5A21-4F05-BC7D-AF01F617B664")
private let atvvTargetName = "小米蓝牙语音遥控器"

private enum TransportEvent: Int32 {
    case ready = 1
    case controlValue = 2
    case audioValue = 3
    case stopWriteAck = 4
    case failure = 5
    case closeDone = 6
}

private enum TransportStatus {
    static let ok: Int32 = 0
    // StopWriteAck status 1 means no write was needed because the remote stop
    // notification had already arrived, or an identical close was queued.
    static let stopAlreadyObserved: Int32 = 1

    static let invalidHandle: Int32 = -1
    static let invalidArgument: Int32 = -2
    static let closing: Int32 = -3
    static let notConnected: Int32 = -10
    static let ambiguousTarget: Int32 = -11
    static let bluetoothUnavailable: Int32 = -12
    static let connectionFailed: Int32 = -13
    static let serviceDiscoveryFailed: Int32 = -14
    static let characteristicUnavailable: Int32 = -15
    static let notificationFailed: Int32 = -16
    static let capabilitiesWriteFailed: Int32 = -17
    static let stopWriteFailed: Int32 = -18
    static let stopStreamMismatch: Int32 = -19
    static let closeWriteUnconfirmed: Int32 = -20
    static let bluetoothPermission: Int32 = -21
    static let bluetoothPoweredOff: Int32 = -22
}

private final class TransportRegistry: @unchecked Sendable {
    static let shared = TransportRegistry()

    private let lock = NSLock()
    private var nextHandle: UInt64 = 1
    private var transports: [UInt64: ATVVTransport] = [:]

    func insert(context: UInt64, callback: MiMacATVVEventCallback) -> (UInt64, ATVVTransport)? {
        lock.lock()
        defer { lock.unlock() }
        guard nextHandle != 0 else { return nil }

        let handle = nextHandle
        nextHandle &+= 1
        let transport = ATVVTransport(handle: handle, context: context, callback: callback)
        transports[handle] = transport
        return (handle, transport)
    }

    func get(_ handle: UInt64) -> ATVVTransport? {
        lock.lock()
        defer { lock.unlock() }
        return transports[handle]
    }

    func remove(_ handle: UInt64, matching transport: ATVVTransport) {
        lock.lock()
        defer { lock.unlock() }
        if transports[handle] === transport {
            transports.removeValue(forKey: handle)
        }
    }
}

private final class ATVVTransport: NSObject, CBCentralManagerDelegate, CBPeripheralDelegate, @unchecked Sendable {
    private enum WriteKind {
        case capabilities
        case microphoneClose(UInt8)
    }

    private let handle: UInt64
    private let context: UInt64
    private let queue: DispatchQueue
    private let admissionLock = NSLock()

    // admissionLock protects only whether new C ABI operations may be queued.
    // CoreBluetooth objects and all transport state below are queue-confined.
    private var acceptingOperations = true
    private var closeRequestCount: UInt64 = 0
    private var callback: MiMacATVVEventCallback?
    private var central: CBCentralManager?
    private var peripheral: CBPeripheral?
    private var transmit: CBCharacteristic?
    private var audio: CBCharacteristic?
    private var control: CBCharacteristic?
    private var connecting = false
    private var selectionAttempted = false
    private var controlNotifying = false
    private var audioNotifying = false
    private var readySent = false
    private var capabilitiesRequested = false
    private var pendingWrite: WriteKind?
    private var queuedCloseStream: UInt8?
    private var activeStreamID: UInt8?
    private var remoteStopObserved = false
    private var closeRequestedForStream: UInt8?
    private var closeSentForStream: UInt8?
    private var closing = false
    private var cancelIssued = false
    private var closeDoneSent = false

    init(handle: UInt64, context: UInt64, callback: MiMacATVVEventCallback) {
        self.handle = handle
        self.context = context
        self.callback = callback
        self.queue = DispatchQueue(label: "dev.mimac.atvv.transport.\(handle)", qos: .userInitiated)
        super.init()
    }

    func start() {
        queue.async { [self] in
            guard !closing, mayAcceptWork else { return }
            central = CBCentralManager(
                delegate: self,
                queue: queue,
                options: [CBCentralManagerOptionShowPowerAlertKey: false]
            )
        }
    }

    func enqueueStop(_ rawStreamID: UInt32) -> Int32 {
        guard rawStreamID <= UInt32(UInt8.max) else { return TransportStatus.invalidArgument }
        admissionLock.lock()
        defer { admissionLock.unlock() }
        guard acceptingOperations else { return TransportStatus.closing }
        let streamID = UInt8(rawStreamID)
        queue.async { [self] in requestStop(streamID) }
        return TransportStatus.ok
    }

    func enqueueClose() -> Int32 {
        admissionLock.lock()
        defer { admissionLock.unlock() }
        // Close remains idempotently retryable until the disconnect barrier is
        // confirmed and CloseDone has been delivered.
        if closeRequestCount < UInt64.max {
            closeRequestCount += 1
        }
        let retry = closeRequestCount > 1
        acceptingOperations = false
        queue.async { [self] in beginClose(retry: retry) }
        return TransportStatus.ok
    }

    private var mayAcceptWork: Bool {
        admissionLock.lock()
        defer { admissionLock.unlock() }
        return acceptingOperations
    }

    func centralManagerDidUpdateState(_ central: CBCentralManager) {
        guard self.central === central else { return }
        if !mayAcceptWork || closing {
            beginClose()
            return
        }
        guard central.state == .poweredOn else {
            switch central.state {
            case .unauthorized:
                emit(.failure, status: TransportStatus.bluetoothPermission)
            case .poweredOff:
                emit(.failure, status: TransportStatus.bluetoothPoweredOff)
            case .unsupported:
                emit(.failure, status: TransportStatus.bluetoothUnavailable)
            default:
                break // Unknown/resetting may become ready without intervention.
            }
            return
        }
        guard !selectionAttempted else { return }
        selectionAttempted = true

        // Only attach to a single, already-connected ATVV remote. This shim
        // never invokes scanForPeripherals or a pairing API.
        let candidates = central.retrieveConnectedPeripherals(withServices: [atvvServiceUUID])
            .filter { $0.name == atvvTargetName }
        guard candidates.count == 1, let candidate = candidates.first else {
            emit(.failure, status: candidates.isEmpty ? TransportStatus.notConnected : TransportStatus.ambiguousTarget)
            return
        }

        peripheral = candidate
        candidate.delegate = self
        connecting = true
        central.connect(candidate, options: nil)
    }

    func centralManager(_ central: CBCentralManager, didConnect peripheral: CBPeripheral) {
        guard self.central === central, self.peripheral === peripheral else { return }
        connecting = false
        if !mayAcceptWork || closing {
            beginClose()
            return
        }
        peripheral.discoverServices([atvvServiceUUID])
    }

    func centralManager(_ central: CBCentralManager, didFailToConnect peripheral: CBPeripheral, error: Error?) {
        guard self.central === central, self.peripheral === peripheral else { return }
        connecting = false
        if closing || !mayAcceptWork {
            beginClose()
            finishCloseAfterConnectionEnded()
            return
        }
        emit(.failure, status: TransportStatus.connectionFailed)
    }

    func centralManager(_ central: CBCentralManager, didDisconnectPeripheral peripheral: CBPeripheral, error: Error?) {
        guard self.central === central, self.peripheral === peripheral else { return }
        connecting = false
        if let pendingWrite {
            reportUnconfirmedWrite(pendingWrite)
            self.pendingWrite = nil
        }
        if closing || !mayAcceptWork {
            beginClose()
            finishCloseAfterConnectionEnded()
        } else {
            emit(.failure, status: TransportStatus.connectionFailed)
        }
    }

    func peripheral(_ peripheral: CBPeripheral, didDiscoverServices error: Error?) {
        guard accepts(peripheral) else { return }
        if error != nil {
            emit(.failure, status: TransportStatus.serviceDiscoveryFailed)
            return
        }
        guard let service = peripheral.services?.first(where: { $0.uuid == atvvServiceUUID }) else {
            emit(.failure, status: TransportStatus.serviceDiscoveryFailed)
            return
        }
        peripheral.discoverCharacteristics([atvvTransmitUUID, atvvAudioUUID, atvvControlUUID], for: service)
    }

    func peripheral(_ peripheral: CBPeripheral, didDiscoverCharacteristicsFor service: CBService, error: Error?) {
        guard accepts(peripheral), service.uuid == atvvServiceUUID else { return }
        if error != nil {
            emit(.failure, status: TransportStatus.characteristicUnavailable)
            return
        }

        for characteristic in service.characteristics ?? [] {
            switch characteristic.uuid {
            case atvvTransmitUUID: transmit = characteristic
            case atvvAudioUUID: audio = characteristic
            case atvvControlUUID: control = characteristic
            default: break
            }
        }

        let controlCanNotify = control.map { $0.properties.contains(.notify) || $0.properties.contains(.indicate) } ?? false
        let audioCanNotify = audio?.properties.contains(.notify) ?? false
        guard let transmit, transmit.properties.contains(.write), let control,
              controlCanNotify, let audio, audioCanNotify else {
            emit(.failure, status: TransportStatus.characteristicUnavailable)
            return
        }
        peripheral.setNotifyValue(true, for: control)
        peripheral.setNotifyValue(true, for: audio)
    }

    func peripheral(_ peripheral: CBPeripheral, didUpdateNotificationStateFor characteristic: CBCharacteristic, error: Error?) {
        guard accepts(peripheral) else { return }
        if error != nil || !characteristic.isNotifying {
            emit(.failure, status: TransportStatus.notificationFailed)
            return
        }
        if characteristic === control {
            controlNotifying = true
        } else if characteristic === audio {
            audioNotifying = true
        } else {
            return
        }
        notifyReadyIfComplete()
    }

    private func notifyReadyIfComplete() {
        guard !readySent, controlNotifying, audioNotifying, mayAcceptWork, !closing else { return }
        readySent = true
        // READY means both subscriptions are confirmed. Go still parses the
        // subsequent Control CAPS value before it can arm its ATVV Source.
        emit(.ready)
        guard mayAcceptWork, !closing, !capabilitiesRequested else { return }
        capabilitiesRequested = true
        guard pendingWrite == nil, let peripheral, let transmit, peripheral.state == .connected else {
            emit(.failure, status: TransportStatus.capabilitiesWriteFailed)
            return
        }
        let capabilitiesQuery = Data([0x0A, 0x01, 0x00, 0x00, 0x03, 0x03])
        pendingWrite = .capabilities
        peripheral.writeValue(capabilitiesQuery, for: transmit, type: .withResponse)
    }

    func peripheral(_ peripheral: CBPeripheral, didUpdateValueFor characteristic: CBCharacteristic, error: Error?) {
        guard accepts(peripheral) else { return }
        guard error == nil, let data = characteristic.value else {
            emit(.failure, status: TransportStatus.notificationFailed)
            return
        }
        if characteristic === control {
            observeControlBoundaries(data)
            emit(.controlValue, bytes: data)
        } else if characteristic === audio {
            emit(.audioValue, bytes: data)
        }
    }

    private func observeControlBoundaries(_ data: Data) {
        guard let opcode = data.first else { return }
        if opcode == 0x00 {
            remoteStopObserved = true
            return
        }
        // The shim only tracks stream boundaries to serialize MIC_CLOSE. Go
        // remains responsible for parsing and validating the full protocol.
        guard opcode == 0x04, data.count >= 4 else { return }
        activeStreamID = data[3]
        remoteStopObserved = false
        closeRequestedForStream = nil
        closeSentForStream = nil
    }

    private func requestStop(_ streamID: UInt8) {
        guard !closing else { return }
        guard activeStreamID == streamID else {
            emit(.stopWriteAck, status: TransportStatus.stopStreamMismatch, bytes: Data([streamID]))
            return
        }
        if remoteStopObserved || closeRequestedForStream == streamID || closeSentForStream == streamID {
            emit(.stopWriteAck, status: TransportStatus.stopAlreadyObserved, bytes: Data([streamID]))
            return
        }
        closeRequestedForStream = streamID
        if pendingWrite != nil {
            queuedCloseStream = streamID
            return
        }
        sendMicrophoneClose(streamID)
    }

    private func sendMicrophoneClose(_ streamID: UInt8, duringClose: Bool = false) {
        guard (!closing || duringClose), activeStreamID == streamID, !remoteStopObserved,
              closeSentForStream != streamID,
              let peripheral, peripheral.state == .connected,
              let transmit, transmit.properties.contains(.write) else {
            if queuedCloseStream == streamID {
                queuedCloseStream = nil
            }
            emit(.stopWriteAck, status: TransportStatus.stopWriteFailed, bytes: Data([streamID]))
            return
        }
        closeSentForStream = streamID
        queuedCloseStream = nil
        pendingWrite = .microphoneClose(streamID)
        peripheral.writeValue(Data([0x0D, streamID]), for: transmit, type: .withResponse)
    }

    func peripheral(_ peripheral: CBPeripheral, didWriteValueFor characteristic: CBCharacteristic, error: Error?) {
        guard self.peripheral === peripheral, characteristic === transmit,
              let write = pendingWrite else { return }
        pendingWrite = nil
        switch write {
        case .capabilities:
            if error != nil {
                emit(.failure, status: TransportStatus.capabilitiesWriteFailed)
            }
        case .microphoneClose(let streamID):
            emit(.stopWriteAck, status: error == nil ? TransportStatus.ok : TransportStatus.stopWriteFailed,
                 bytes: Data([streamID]))
        }

        if let queuedCloseStream {
            sendMicrophoneClose(queuedCloseStream, duringClose: closing)
        }
        if closing {
            advanceClose()
        }
    }

    private func reportUnconfirmedWrite(_ write: WriteKind) {
        switch write {
        case .capabilities:
            if !closing { emit(.failure, status: TransportStatus.capabilitiesWriteFailed) }
        case .microphoneClose(let streamID):
            emit(.stopWriteAck, status: TransportStatus.closeWriteUnconfirmed, bytes: Data([streamID]))
        }
    }

    private func accepts(_ peripheral: CBPeripheral) -> Bool {
        guard self.peripheral === peripheral, !closing, mayAcceptWork else {
            if !mayAcceptWork && !closing { beginClose() }
            return false
        }
        return true
    }

    private func beginClose(retry: Bool = false) {
        guard !closeDoneSent else { return }
        closing = true
        advanceClose(retry: retry)
    }

    private func advanceClose(retry: Bool = false) {
        guard closing, !closeDoneSent else { return }
        guard let central, let peripheral else {
            finishCloseAfterConnectionEnded()
            return
        }

        if let activeStreamID, !remoteStopObserved, closeSentForStream != activeStreamID {
            closeRequestedForStream = activeStreamID
            if pendingWrite == nil {
                sendMicrophoneClose(activeStreamID, duringClose: true)
            } else {
                queuedCloseStream = activeStreamID
            }
        }
        if let queuedCloseStream, remoteStopObserved, activeStreamID == queuedCloseStream {
            self.queuedCloseStream = nil
            emit(.stopWriteAck, status: TransportStatus.stopAlreadyObserved, bytes: Data([queuedCloseStream]))
        }
        if let queuedCloseStream, pendingWrite == nil {
            sendMicrophoneClose(queuedCloseStream, duringClose: true)
        }

        if peripheral.state == .disconnected {
            finishCloseAfterConnectionEnded()
            return
        }
        // Give an in-flight native write one asynchronous chance to report its
        // acknowledgement. A Go-side bounded retry escalates to GATT cancel.
        if pendingWrite != nil && !retry { return }

        if controlNotifying, let control { peripheral.setNotifyValue(false, for: control) }
        if audioNotifying, let audio { peripheral.setNotifyValue(false, for: audio) }
        controlNotifying = false
        audioNotifying = false

        switch peripheral.state {
        case .connected, .connecting, .disconnecting:
            if !cancelIssued || retry {
                cancelIssued = true
                central.cancelPeripheralConnection(peripheral)
            }
        case .disconnected:
            finishCloseAfterConnectionEnded()
        @unknown default:
            if !cancelIssued {
                cancelIssued = true
                central.cancelPeripheralConnection(peripheral)
            }
        }
    }

    private func finishCloseAfterConnectionEnded() {
        guard closing, !closeDoneSent else { return }
        if let pendingWrite {
            reportUnconfirmedWrite(pendingWrite)
            self.pendingWrite = nil
        }
        if let queuedCloseStream {
            self.queuedCloseStream = nil
            emit(.stopWriteAck, status: TransportStatus.stopWriteFailed, bytes: Data([queuedCloseStream]))
        }
        connecting = false
        if peripheral?.delegate === self {
            peripheral?.delegate = nil
        }
        if central?.delegate === self {
            central?.delegate = nil
        }

        closeDoneSent = true
        let finalCallback = callback
        callback = nil
        let keepAlive = self
        finalCallback?(context, TransportEvent.closeDone.rawValue, TransportStatus.ok, nil, 0)
        TransportRegistry.shared.remove(handle, matching: self)
        withExtendedLifetime(keepAlive) {}
    }

    private func emit(_ event: TransportEvent, status: Int32 = TransportStatus.ok, bytes: Data? = nil) {
        guard let callback else { return }
        guard let bytes, !bytes.isEmpty else {
            callback(context, event.rawValue, status, nil, 0)
            return
        }
        guard bytes.count <= Int(Int32.max) else {
            callback(context, TransportEvent.failure.rawValue, TransportStatus.invalidArgument, nil, 0)
            return
        }
        bytes.withUnsafeBytes { buffer in
            let pointer = buffer.bindMemory(to: UInt8.self).baseAddress
            callback(context, event.rawValue, status, pointer, Int32(bytes.count))
        }
    }
}

@_cdecl("MiMacATVVTransportCreate")
public func MiMacATVVTransportCreate(_ context: UInt64, _ callback: MiMacATVVEventCallback?) -> UInt64 {
    guard let callback, let (handle, transport) = TransportRegistry.shared.insert(context: context, callback: callback) else {
        return 0
    }
    transport.start()
    return handle
}

@_cdecl("MiMacATVVTransportRequestStop")
public func MiMacATVVTransportRequestStop(_ handle: UInt64, _ streamID: UInt32) -> Int32 {
    guard let transport = TransportRegistry.shared.get(handle) else { return TransportStatus.invalidHandle }
    return transport.enqueueStop(streamID)
}

@_cdecl("MiMacATVVTransportClose")
public func MiMacATVVTransportClose(_ handle: UInt64) -> Int32 {
    guard let transport = TransportRegistry.shared.get(handle) else { return TransportStatus.invalidHandle }
    return transport.enqueueClose()
}
