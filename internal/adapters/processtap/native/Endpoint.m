#import <Foundation/Foundation.h>
#import <CoreAudio/CoreAudio.h>
#import <CoreAudio/AudioHardwareTapping.h>
#import <CoreAudio/CATapDescription.h>
#import <unistd.h>

// Metadata/resource owner only: no input IOProc, microphone, PCM storage or
// default-device mutation. The caller primes its own output before Create.
typedef struct {
    AudioObjectID tap;
    AudioObjectID aggregate;
    CFStringRef uid;
} MiMacTapEndpoint;

static AudioObjectPropertyAddress address(AudioObjectPropertySelector selector) {
    return (AudioObjectPropertyAddress){selector, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
}

int32_t MiMacProcessTapClose(uintptr_t raw) {
    if (!raw) return noErr;
    MiMacTapEndpoint *owner = (MiMacTapEndpoint *)raw;
    if (owner->aggregate) {
        OSStatus status = AudioHardwareDestroyAggregateDevice(owner->aggregate);
        if (status) return status; // Retain both objects for an explicit retry.
        owner->aggregate = kAudioObjectUnknown;
    }
    if (owner->tap) {
        OSStatus status = AudioHardwareDestroyProcessTap(owner->tap);
        if (status) return status;
        owner->tap = kAudioObjectUnknown;
    }
    if (owner->uid) CFRelease(owner->uid);
    free(owner);
    return noErr;
}

uintptr_t MiMacProcessTapCreate(int32_t *result) {
    @autoreleasepool {
        if (!result) return 0;
        *result = kAudioHardwareUnspecifiedError;
        MiMacTapEndpoint *owner = calloc(1, sizeof(*owner));
        if (!owner) return 0;
        pid_t pid = getpid();
        AudioObjectID process = kAudioObjectUnknown;
        UInt32 size = sizeof(process);
        AudioObjectPropertyAddress a = address(kAudioHardwarePropertyTranslatePIDToProcessObject);
        OSStatus status = AudioObjectGetPropertyData(kAudioObjectSystemObject, &a, sizeof(pid), &pid, &size, &process);
        if (!status && !process) status = kAudioHardwareBadObjectError;
        if (!status) {
            CATapDescription *description = [[CATapDescription alloc] initStereoMixdownOfProcesses:@[@(process)]];
            description.exclusive = NO;
            description.privateTap = NO;
            description.muteBehavior = CATapMuted;
            description.name = @"MiMac Remote Audio";
            status = AudioHardwareCreateProcessTap(description, &owner->tap);
        }
        if (!status) {
            CATapDescription *actual = nil;
            size = sizeof(actual);
            a = address(kAudioTapPropertyDescription);
            status = AudioObjectGetPropertyData(owner->tap, &a, 0, NULL, &size, &actual);
            if (!status && (!actual || actual.privateTap || actual.exclusive || actual.muteBehavior != CATapMuted ||
                            actual.processes.count != 1 || ![actual.processes[0] isEqual:@(process)]))
                status = kAudioHardwareIllegalOperationError;
        }
        CFStringRef tapUID = NULL;
        if (!status) {
            size = sizeof(tapUID);
            a = address(kAudioTapPropertyUID);
            status = AudioObjectGetPropertyData(owner->tap, &a, 0, NULL, &size, &tapUID);
            if (!status && !tapUID) status = kAudioHardwareBadObjectError;
        }
        if (!status) {
            NSString *uid = [@"dev.mimac.input." stringByAppendingString:NSUUID.UUID.UUIDString];
            owner->uid = CFBridgingRetain(uid);
            NSDictionary *composition = @{
                @kAudioAggregateDeviceNameKey: @"MiMac Remote Audio",
                @kAudioAggregateDeviceUIDKey: uid,
                @kAudioAggregateDeviceIsPrivateKey: @NO,
                @kAudioAggregateDeviceTapListKey: @[@{@kAudioSubTapUIDKey: (__bridge NSString *)tapUID}]
            };
            status = AudioHardwareCreateAggregateDevice((__bridge CFDictionaryRef)composition, &owner->aggregate);
        }
        if (tapUID) CFRelease(tapUID);
        if (!status) {
            CFStringRef actualUID = NULL;
            size = sizeof(actualUID);
            a = address(kAudioDevicePropertyDeviceUID);
            status = AudioObjectGetPropertyData(owner->aggregate, &a, 0, NULL, &size, &actualUID);
            if (!status && (!actualUID || !CFEqual(actualUID, owner->uid))) status = kAudioHardwareIllegalOperationError;
            if (actualUID) CFRelease(actualUID);
        }
        *result = status;
        // Return partial ownership on failure. Go must close/retry rather than
        // lose a public device or unmute the process before output has stopped.
        if (status && !owner->tap && !owner->aggregate) {
            if (owner->uid) CFRelease(owner->uid);
            free(owner);
            return 0;
        }
        return (uintptr_t)owner;
    }
}

int32_t MiMacProcessTapCopyUID(uintptr_t raw, char *destination, int32_t capacity) {
    MiMacTapEndpoint *owner = (MiMacTapEndpoint *)raw;
    if (!owner || !owner->uid || !destination || capacity <= 0) return kAudioHardwareIllegalOperationError;
    return CFStringGetCString(owner->uid, destination, capacity, kCFStringEncodingUTF8) ? noErr : kAudioHardwareBadPropertySizeError;
}
