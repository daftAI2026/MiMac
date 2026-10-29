#import "SiriGlassCompatibility.h"

#import <dlfcn.h>
#import <stdio.h>
#import <string.h>

static const char *kSwiftUICore = "/System/Library/Frameworks/SwiftUICore.framework/SwiftUICore";
static const char *kSwiftRuntime = "/usr/lib/swift/libswiftCore.dylib";
static void *g_swiftUICore;
static void *g_swiftRuntime;

static const char *kRequiredSymbols[] = {
    "$s7SwiftUI6_GlassV4siriACvgZ",
    "$s7SwiftUI6_GlassV5clearACvgZ",
    "$s7SwiftUI5GlassV7regularACvgZ",
    "$s7SwiftUI6_GlassV13ContentEffectV5lenseAEvgZ",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11anchorPoint7opacity11personality15audioPowerLevelAeA04UnitH0V_SfAE11PersonalityVSftcfC",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11AudioMetersV25normalizedPowerAmplitudes17stopFrequenciesHzAGSaySfG_AJtcfC",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11anchorPoint7opacity11personality11audioMetersAeA04UnitH0V_SfAE11PersonalityVAE05AudioL0VtcfC",
    "$s7SwiftUI6_GlassV15siriWaveOptionsyA2C04SirieF0VSgF",
    "$s7SwiftUI6_GlassV13contentEffectyA2C07ContentE0VSgF",
    "$s7SwiftUI6_GlassV10subvariantyACSSSgF",
    "$s7SwiftUI6_GlassV15excludingShadowyACSbF",
    "$s7SwiftUI6_GlassVMa",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV8thinkingAGvgZ",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV9attendingAGvgZ",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV10respondingAGvgZ",
    "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV10unresolvedAGvgZ",
    "$s7SwiftUI5GlassV8explicitAcA01_C0V_tcfC",
};

static void set_error(char *buffer, int32_t capacity, const char *format, const char *detail) {
    if (!buffer || capacity <= 0) return;
    if (detail) snprintf(buffer, (size_t)capacity, format, detail);
    else snprintf(buffer, (size_t)capacity, "%s", format);
}

typedef struct {
    void *metadata;
    uint64_t state;
} GlassMetadataResponse;

typedef GlassMetadataResponse (*GlassMetadataAccessor)(uint64_t);

static int checkValueLayout(void *framework,
                            const char *metadataSymbol,
                            const char *typeName,
                            uint64_t expectedSize,
                            char *errorBuffer,
                            int32_t errorCapacity) {
    GlassMetadataAccessor accessor = (GlassMetadataAccessor)dlsym(framework, metadataSymbol);
    if (!accessor) {
        set_error(errorBuffer, errorCapacity, "required SwiftUICore metadata accessor is missing: %s", metadataSymbol);
        return 0;
    }
    GlassMetadataResponse response = accessor(0);
    if (!response.metadata) {
        set_error(errorBuffer, errorCapacity, "SwiftUICore metadata accessor returned no metadata: %s", metadataSymbol);
        return 0;
    }
    void **valueWitnessTable = ((void **)response.metadata)[-1];
    if (!valueWitnessTable) {
        set_error(errorBuffer, errorCapacity, "SwiftUICore value-witness table is missing: %s", typeName);
        return 0;
    }
    uint64_t size = ((uint64_t *)valueWitnessTable)[8];
    uint64_t stride = ((uint64_t *)valueWitnessTable)[9];
    if (size != expectedSize || stride != expectedSize || !valueWitnessTable[1]) {
        if (errorBuffer && errorCapacity > 0) {
            snprintf(errorBuffer, (size_t)errorCapacity,
                     "%s layout/destroy witness mismatch: expected %llu/%llu with destroy; got %llu/%llu destroy=%s",
                     typeName,
                     (unsigned long long)expectedSize,
                     (unsigned long long)expectedSize,
                     (unsigned long long)size,
                     (unsigned long long)stride,
                     valueWitnessTable[1] ? "present" : "missing");
        }
        return 0;
    }
    return 1;
}

int32_t SiriGlassCheckPrivateCompatibility(uint64_t swiftSize,
                                           uint64_t swiftStride,
                                           uint64_t *systemSize,
                                           uint64_t *systemStride,
                                           char *errorBuffer,
                                           int32_t errorCapacity) {
    if (!g_swiftUICore) {
        g_swiftUICore = dlopen(kSwiftUICore, RTLD_NOW | RTLD_LOCAL);
        if (!g_swiftUICore) {
            const char *detail = dlerror();
            set_error(errorBuffer, errorCapacity, "cannot load installed SwiftUICore: %s", detail ? detail : "unknown loader error");
            return 0;
        }
    }

    for (size_t i = 0; i < sizeof(kRequiredSymbols) / sizeof(kRequiredSymbols[0]); i++) {
        if (!dlsym(g_swiftUICore, kRequiredSymbols[i])) {
            set_error(errorBuffer, errorCapacity, "required SwiftUICore symbol is missing: %s", kRequiredSymbols[i]);
            return 0;
        }
    }
    // The array-meter adapter makes an owned copy of each Array<Float>
    // storage reference before calling the captured consuming initializer.
    // This is a Swift runtime entry point rather than a SwiftUICore export.
    if (!g_swiftRuntime) {
        g_swiftRuntime = dlopen(kSwiftRuntime, RTLD_NOW | RTLD_LOCAL);
        if (!g_swiftRuntime) {
            const char *detail = dlerror();
            set_error(errorBuffer, errorCapacity, "cannot load Swift runtime: %s", detail ? detail : "unknown loader error");
            return 0;
        }
    }
    if (!dlsym(g_swiftRuntime, "swift_retain")) {
        set_error(errorBuffer, errorCapacity, "required Swift runtime symbol is missing: swift_retain", NULL);
        return 0;
    }

    GlassMetadataAccessor accessor = (GlassMetadataAccessor)dlsym(g_swiftUICore, "$s7SwiftUI6_GlassVMa");
    if (!accessor) {
        set_error(errorBuffer, errorCapacity, "private _Glass metadata accessor is missing", NULL);
        return 0;
    }
    GlassMetadataResponse response = accessor(0);
    if (!response.metadata) {
        set_error(errorBuffer, errorCapacity, "private _Glass metadata accessor returned no metadata", NULL);
        return 0;
    }

    void **valueWitnessTable = ((void **)response.metadata)[-1];
    if (!valueWitnessTable) {
        set_error(errorBuffer, errorCapacity, "private _Glass value-witness table is unavailable", NULL);
        return 0;
    }
    uint64_t metadataSize = ((uint64_t *)valueWitnessTable)[8];
    uint64_t metadataStride = ((uint64_t *)valueWitnessTable)[9];
    void *destroyWitness = valueWitnessTable[1];
    if (systemSize) *systemSize = metadataSize;
    if (systemStride) *systemStride = metadataStride;

    if (metadataSize != 40 || metadataStride != 40 || !destroyWitness) {
        if (errorBuffer && errorCapacity > 0) {
            snprintf(errorBuffer, (size_t)errorCapacity,
                     "private _Glass metadata mismatch: expected size/stride 40/40 and destroy witness; got %llu/%llu destroy=%s",
                     (unsigned long long)metadataSize, (unsigned long long)metadataStride,
                     destroyWitness ? "present" : "missing");
        }
        return 0;
    }

    if (metadataSize != swiftSize || metadataStride != swiftStride) {
        if (errorBuffer && errorCapacity > 0) {
            snprintf(errorBuffer, (size_t)errorCapacity,
                     "SwiftUI.Glass layout mismatch: Swift size/stride=%llu/%llu, metadata=%llu/%llu",
                     (unsigned long long)swiftSize, (unsigned long long)swiftStride,
                     (unsigned long long)metadataSize, (unsigned long long)metadataStride);
        }
        return 0;
    }
    if (!checkValueLayout(g_swiftUICore,
                          "$s7SwiftUI6_GlassV15SiriWaveOptionsV11AudioMetersVMa",
                          "SiriWaveOptions.AudioMeters",
                          16,
                          errorBuffer,
                          errorCapacity)) return 0;
    if (!checkValueLayout(g_swiftUICore,
                          "$s7SwiftUI6_GlassV15SiriWaveOptionsVMa",
                          "SiriWaveOptions",
                          48,
                          errorBuffer,
                          errorCapacity)) return 0;
    if (errorBuffer && errorCapacity > 0) errorBuffer[0] = '\0';
    return 1;
}
