import CoreGraphics
import Foundation

// Native construction only. Never posts events or requests input access.
let marker: Int64 = 0x4d694d61634b6579
let source = CGEventSource(stateID: .privateState)!
source.userData = marker
source.localEventsSuppressionInterval = 0
var rows: [[String: Any]] = []

for code: CGKeyCode in [54, 61, 63, 0] {
    for down in [true, false] {
        let event = CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: down)!
        let typeBeforeFlags = event.type.rawValue
        let flags: UInt64 = code == 54 ? 0x100010 : code == 61 ? 0x80040 : 0
        event.flags = CGEventFlags(rawValue: down ? flags : 0)
        event.setIntegerValueField(.eventSourceUserData, value: marker)
        rows.append([
            "code": code,
            "constructor_down": down,
            "type_before_flags": typeBeforeFlags,
            "type_after_flags": event.type.rawValue,
            "flags": event.flags.rawValue,
            "event_user_data": event.getIntegerValueField(.eventSourceUserData)
        ])
    }
}

let result: [String: Any] = [
    "scope": "construct and inspect only; no CGEventPost or microphone",
    "enum_flagsChanged": CGEventType.flagsChanged.rawValue,
    "enum_keyDown": CGEventType.keyDown.rawValue,
    "enum_keyUp": CGEventType.keyUp.rawValue,
    "rows": rows
]
let data = try JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])
print(String(data: data, encoding: .utf8)!)
