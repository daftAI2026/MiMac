/*
 Private material adapter for the explicitly supported local system ABI.
 See DERIVATION.md for provenance and ownership changes. This copy deliberately
 has no signal recovery or process-wide debug output.
*/
#import <Foundation/Foundation.h>
#include "GlassPrivateABI.h"
#import <dlfcn.h>
#import <math.h>
#import <stdio.h>
#import <stdint.h>
#import <stdlib.h>
#import <string.h>

void sw_call_x8(void *fn, void *out);
void sw_call_x20_x0_x8(void *fn, void *self, void *arg0, void *out);
void sw_call_x20_w0_x8(void *fn, void *self, uint32_t arg0, void *out);
void sw_call_x0_x1_x8(void *fn, void *arg0, void *arg1, void *out);
void sw_options_init_power(void *fn, void *out, void *personality, const void *args);
void sw_options_init_meters(void *fn, void *out, void *personality,
                            const void *meters, const void *anchorOpacity);

void *g_subvariant_fn;
void *g_glass_explicit_init_fn;

enum { kGlassSize = 40, kOptionsSize = 48, kAudioMetersSize = 16 };

static const char *kCore = "/System/Library/Frameworks/SwiftUICore.framework/SwiftUICore";
static const char *kSwiftRuntime = "/usr/lib/swift/libswiftCore.dylib";

static void (*g_destroy)(void *value, void *metadata);
static void *(*g_swift_retain)(void *object);
static void *g_glass_md;
static void *g_audio_meters_md;
static void *g_options_md;
static void (*g_audio_meters_destroy)(void *value, void *metadata);
static void (*g_options_destroy)(void *value, void *metadata);
static void *g_dl;
static void *g_swift_runtime;
static void *g_siri;
static void *g_clear;
static void *g_lense;
static void *g_opt_init;
static void *g_audio_meters_init;
static void *g_options_meters_init;
static void *g_wave;
static void *g_effect;
static void *g_excluding_shadow;
static void *g_personality[4];
static uint8_t g_personality_byte[4];
static int g_ready;
static char g_error[512];

const char *glass_last_error(void) { return g_error; }

static void set_missing_symbol(const char *symbol) {
    snprintf(g_error, sizeof g_error, "required SwiftUICore symbol is missing: %s", symbol);
}

static void *resolve_symbol(const char *symbol) {
    void *value = dlsym(g_dl, symbol);
    if (!value) set_missing_symbol(symbol);
    return value;
}

typedef struct {
    void *metadata;
    uint64_t state;
} GlassMetadataResponse;

typedef GlassMetadataResponse (*GlassMetadataAccessor)(uint64_t);

static int capture_value_layout(const char *metadataSymbol,
                                const char *typeName,
                                uint64_t expectedSize,
                                void **metadataOut,
                                void (**destroyOut)(void *, void *)) {
    GlassMetadataAccessor accessor = (GlassMetadataAccessor)dlsym(g_dl, metadataSymbol);
    if (!accessor) {
        set_missing_symbol(metadataSymbol);
        return 0;
    }
    GlassMetadataResponse response = accessor(0);
    if (!response.metadata) {
        snprintf(g_error, sizeof g_error, "%s metadata accessor returned no metadata", typeName);
        return 0;
    }
    void **valueWitnessTable = ((void **)response.metadata)[-1];
    if (!valueWitnessTable) {
        snprintf(g_error, sizeof g_error, "%s value-witness table is unavailable", typeName);
        return 0;
    }
    uint64_t size = ((uint64_t *)valueWitnessTable)[8];
    uint64_t stride = ((uint64_t *)valueWitnessTable)[9];
    if (size != expectedSize || stride != expectedSize || !valueWitnessTable[1]) {
        snprintf(g_error, sizeof g_error,
                 "%s layout/destroy witness mismatch: expected %llu/%llu with destroy; got %llu/%llu destroy=%s",
                 typeName,
                 (unsigned long long)expectedSize,
                 (unsigned long long)expectedSize,
                 (unsigned long long)size,
                 (unsigned long long)stride,
                 valueWitnessTable[1] ? "present" : "missing");
        return 0;
    }
    *metadataOut = response.metadata;
    *destroyOut = (void (*)(void *, void *))valueWitnessTable[1];
    return 1;
}

static int ensure(void) {
    if (g_ready) return 1;

    if (!g_dl) g_dl = dlopen(kCore, RTLD_NOW | RTLD_LOCAL);
    if (!g_dl) {
        const char *detail = dlerror();
        snprintf(g_error, sizeof g_error, "cannot load installed SwiftUICore: %s", detail ? detail : "unknown loader error");
        return 0;
    }

#define RESOLVE(target, symbol) do { \
    target = resolve_symbol(symbol); \
    if (!(target)) return 0; \
} while (0)

    if (!g_swift_runtime) g_swift_runtime = dlopen(kSwiftRuntime, RTLD_NOW | RTLD_LOCAL);
    if (!g_swift_runtime) {
        const char *detail = dlerror();
        snprintf(g_error, sizeof g_error, "cannot load Swift runtime: %s", detail ? detail : "unknown loader error");
        return 0;
    }
    g_swift_retain = (void *(*)(void *))dlsym(g_swift_runtime, "swift_retain");
    if (!g_swift_retain) {
        set_missing_symbol("swift_retain");
        return 0;
    }

    RESOLVE(g_siri, "$s7SwiftUI6_GlassV4siriACvgZ");
    RESOLVE(g_clear, "$s7SwiftUI6_GlassV5clearACvgZ");
    RESOLVE(g_lense, "$s7SwiftUI6_GlassV13ContentEffectV5lenseAEvgZ");
    RESOLVE(g_opt_init, "$s7SwiftUI6_GlassV15SiriWaveOptionsV11anchorPoint7opacity11personality15audioPowerLevelAeA04UnitH0V_SfAE11PersonalityVSftcfC");
    RESOLVE(g_audio_meters_init, "$s7SwiftUI6_GlassV15SiriWaveOptionsV11AudioMetersV25normalizedPowerAmplitudes17stopFrequenciesHzAGSaySfG_AJtcfC");
    RESOLVE(g_options_meters_init, "$s7SwiftUI6_GlassV15SiriWaveOptionsV11anchorPoint7opacity11personality11audioMetersAeA04UnitH0V_SfAE11PersonalityVAE05AudioL0VtcfC");
    RESOLVE(g_wave, "$s7SwiftUI6_GlassV15siriWaveOptionsyA2C04SirieF0VSgF");
    RESOLVE(g_effect, "$s7SwiftUI6_GlassV13contentEffectyA2C07ContentE0VSgF");
    RESOLVE(g_subvariant_fn, "$s7SwiftUI6_GlassV10subvariantyACSSSgF");
    RESOLVE(g_excluding_shadow, "$s7SwiftUI6_GlassV15excludingShadowyACSbF");
    RESOLVE(g_glass_explicit_init_fn, "$s7SwiftUI5GlassV8explicitAcA01_C0V_tcfC");
    for (int i = 0; i < 4; i++) {
        static const char *personalitySymbols[4] = {
            "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV8thinkingAGvgZ",
            "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV9attendingAGvgZ",
            "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV10respondingAGvgZ",
            "$s7SwiftUI6_GlassV15SiriWaveOptionsV11PersonalityV10unresolvedAGvgZ",
        };
        g_personality[i] = resolve_symbol(personalitySymbols[i]);
        if (!g_personality[i]) return 0;
    }

    GlassMetadataAccessor metadataAccessor = (GlassMetadataAccessor)dlsym(g_dl, "$s7SwiftUI6_GlassVMa");
    if (!metadataAccessor) {
        set_missing_symbol("$s7SwiftUI6_GlassVMa");
        return 0;
    }
    GlassMetadataResponse response = metadataAccessor(0);
    if (!response.metadata) {
        snprintf(g_error, sizeof g_error, "private _Glass metadata accessor returned no metadata");
        return 0;
    }
    void **valueWitnessTable = ((void **)response.metadata)[-1];
    if (!valueWitnessTable) {
        snprintf(g_error, sizeof g_error, "private _Glass value-witness table is unavailable");
        return 0;
    }
    uint64_t size = ((uint64_t *)valueWitnessTable)[8];
    uint64_t stride = ((uint64_t *)valueWitnessTable)[9];
    if (size != kGlassSize || stride != kGlassSize || !valueWitnessTable[1]) {
        snprintf(g_error, sizeof g_error,
                 "private _Glass layout/witness mismatch: expected 40/40 with destroy witness; got %llu/%llu destroy=%s",
                 (unsigned long long)size, (unsigned long long)stride,
                 valueWitnessTable[1] ? "present" : "missing");
        return 0;
    }

    g_glass_md = response.metadata;
    g_destroy = (void (*)(void *, void *))valueWitnessTable[1];
    if (!capture_value_layout("$s7SwiftUI6_GlassV15SiriWaveOptionsV11AudioMetersVMa",
                              "SiriWaveOptions.AudioMeters", 16,
                              &g_audio_meters_md, &g_audio_meters_destroy)) return 0;
    if (!g_audio_meters_md || !g_audio_meters_destroy) {
        snprintf(g_error, sizeof g_error, "SiriWaveOptions.AudioMeters metadata/destroy gate failed");
        return 0;
    }
    if (!capture_value_layout("$s7SwiftUI6_GlassV15SiriWaveOptionsVMa",
                              "SiriWaveOptions", 48,
                              &g_options_md, &g_options_destroy)) return 0;
    for (int i = 0; i < 4; i++) {
        uint8_t byte = 0;
        sw_call_x8(g_personality[i], &byte);
        g_personality_byte[i] = byte;
    }

#undef RESOLVE
    g_error[0] = '\0';
    g_ready = 1;
    return 1;
}

static int release_glass(void *glass) {
    if (!glass) return 1;
    if (!g_destroy || !g_glass_md) {
        snprintf(g_error, sizeof g_error, "cannot destroy private _Glass without a verified destroy witness");
        return 0;
    }
    g_destroy(glass, g_glass_md);
    free(glass);
    return 1;
}

static int allocate_glass(void **out, const char *label) {
    *out = NULL;
    int result = posix_memalign(out, 16, kGlassSize);
    if (result != 0) {
        snprintf(g_error, sizeof g_error, "allocate %s failed: %s", label, strerror(result));
        return 0;
    }
    memset(*out, 0, kGlassSize);
    return 1;
}

static int allocate_value(void **out, size_t size, const char *label) {
    *out = NULL;
    int result = posix_memalign(out, 16, size);
    if (result != 0) {
        snprintf(g_error, sizeof g_error, "allocate %s failed: %s", label, strerror(result));
        return 0;
    }
    memset(*out, 0, size);
    return 1;
}

// Swift Array<Float> storage layout observed on the gated macOS 27.0.1
// (26A434) arm64 runtime: count at +0x10 and elements at +0x20.
// The model performs the same bounds checks before this private ABI boundary;
// repeat them here so malformed arrays never reach SwiftUICore's trapping init.
static int validate_meter_array(const void *storage, const char *label,
                                uint64_t *countOut, const float **valuesOut) {
    if (!storage || ((uintptr_t)storage & (sizeof(void *) - 1)) != 0) {
        snprintf(g_error, sizeof g_error, "%s Array<Float> storage is null or misaligned", label);
        return 0;
    }
    const uint8_t *bytes = (const uint8_t *)storage;
    uint64_t count = *(const uint64_t *)(bytes + 0x10);
    if (count == 0 || count > 64) {
        snprintf(g_error, sizeof g_error, "%s meter count must be between 1 and 64", label);
        return 0;
    }
    *countOut = count;
    *valuesOut = (const float *)(bytes + 0x20);
    return 1;
}

static int validate_meter_values(const void *amplitudesStorage,
                                 const void *frequenciesStorage) {
    uint64_t amplitudeCount = 0;
    uint64_t frequencyCount = 0;
    const float *amplitudes = NULL;
    const float *frequencies = NULL;
    if (!validate_meter_array(amplitudesStorage, "amplitude", &amplitudeCount, &amplitudes) ||
        !validate_meter_array(frequenciesStorage, "frequency", &frequencyCount, &frequencies)) return 0;
    if (amplitudeCount != frequencyCount) {
        snprintf(g_error, sizeof g_error, "audio meter amplitude and frequency counts must match");
        return 0;
    }
    float previous = 0.0f;
    for (uint64_t index = 0; index < amplitudeCount; index++) {
        float amplitude = amplitudes[index];
        float frequency = frequencies[index];
        if (!isfinite(amplitude) || amplitude < 0.0f || amplitude > 1.0f) {
            snprintf(g_error, sizeof g_error, "audio meter amplitudes must be finite and between 0 and 1");
            return 0;
        }
        if (!isfinite(frequency) || frequency <= previous) {
            snprintf(g_error, sizeof g_error, "audio meter frequencies must be finite, positive and strictly increasing");
            return 0;
        }
        previous = frequency;
    }
    return 1;
}

// kind: 0 siri, 1 siri+lense, 2 siri+wave, 3 siri+lense+wave, 4 private clear.
// Public Glass.regular (host kind 5) is constructed directly by the Swift caller.
// Returns an aligned C buffer containing a live private _Glass value.
void *glass_compose(int32_t kind, int32_t personality, float db) {
    if (!ensure()) return NULL;
    g_error[0] = '\0';
    if (kind < 0 || kind > 4) {
        snprintf(g_error, sizeof g_error, "private kind must be between 0 and 4");
        return NULL;
    }
    if (personality < 0 || personality > 3) {
        snprintf(g_error, sizeof g_error, "personality must be between 0 and 3");
        return NULL;
    }
    if (!isfinite(db) || db < -160.0f || db > 0.0f) {
        snprintf(g_error, sizeof g_error, "audioPowerDB must be finite and between -160 and 0 dB");
        return NULL;
    }

    void *glass = NULL;
    void *next = NULL;
    if (!allocate_glass(&glass, "base _Glass")) return NULL;
    void *getter = g_siri;
    if (kind == 4) getter = g_clear;
    sw_call_x8(getter, glass);

    int want_lens = kind == 1 || kind == 3;
    int want_wave = kind == 2 || kind == 3;
    if (want_lens) {
        if (!allocate_glass(&next, "content-effect _Glass")) {
            release_glass(glass);
            return NULL;
        }
        uint8_t effect = 0;
        sw_call_x8(g_lense, &effect);
        sw_call_x20_x0_x8(g_effect, glass, &effect, next);
        release_glass(glass);
        glass = next;
        next = NULL;
    }

    if (want_wave) {
        uint8_t options[kOptionsSize] = {0};
        typedef struct {
            double anchorX;
            double anchorY;
            float opacity;
            float audioPowerDB;
        } OptionsArguments;
        OptionsArguments arguments = {0.5, 0.5, 1.0f, db};
        sw_options_init_power(g_opt_init, options, &g_personality_byte[personality], &arguments);
        if (!allocate_glass(&next, "SiriWave _Glass")) {
            release_glass(glass);
            return NULL;
        }
        sw_call_x20_x0_x8(g_wave, glass, options, next);
        release_glass(glass);
        glass = next;
    }

    g_error[0] = '\0';
    return glass;
}

void *glass_alloc(void) {
    if (!ensure()) return NULL;
    void *value = NULL;
    if (!allocate_glass(&value, "subvariant _Glass")) return NULL;
    return value;
}

// Start the captured array-meter composition from the public Siri base.
// The returned private value owns one Glass value in an aligned C allocation.
void *glass_siri_base(void) {
    if (!ensure()) return NULL;
    g_error[0] = '\0';
    void *value = NULL;
    if (!allocate_glass(&value, "base Siri _Glass")) return NULL;
    sw_call_x8(g_siri, value);
    g_error[0] = '\0';
    return value;
}

// Apply frequency-array SiriWaveOptions to an existing private Siri Glass.
// The input storage pointers are borrowed. This helper retains each one just
// before AudioMeters.init, which consumes those two references. That init's
// value is consumed by SiriWaveOptions.init(audioMeters:). The subsequent
// _Glass.siriWaveOptions setter borrows its input options and retains another
// reference to both arrays in the returned _Glass. Destroy caller options
// after the setter; the returned Glass remains independently owned.
//
// Captured call parameters: UnitPoint.center and opacity 1.0. They are fixed
// construction values from the reference call path, not user-facing dB input.
void *glass_apply_audio_meters_wave(const void *glass,
                                    const void *amplitudesStorage,
                                    const void *frequenciesStorage,
                                    int32_t personality) {
    if (!ensure()) return NULL;
    g_error[0] = '\0';
    if (!glass) {
        snprintf(g_error, sizeof g_error, "SiriWaveOptions received a null _Glass value");
        return NULL;
    }
    if (personality < 0 || personality > 3) {
        snprintf(g_error, sizeof g_error, "personality must be between 0 and 3");
        return NULL;
    }
    if (!validate_meter_values(amplitudesStorage, frequenciesStorage)) return NULL;

    // Allocate everything before retaining or transferring array ownership.
    // After AudioMeters.init succeeds, the remaining captured Swift calls have
    // no recoverable error return; only an ABI mismatch can trap, which the
    // exact OS/build/symbol/metadata gates are intended to prevent.
    void *meters = NULL;
    void *options = NULL;
    void *result = NULL;
    if (!allocate_value(&meters, kAudioMetersSize, "AudioMeters")) return NULL;
    if (!allocate_value(&options, kOptionsSize, "SiriWaveOptions")) {
        free(meters);
        return NULL;
    }
    if (!allocate_glass(&result, "SiriWaveOptions _Glass")) {
        free(options);
        free(meters);
        return NULL;
    }

    // AudioMeters.init consumes one reference for each [Float] argument.
    g_swift_retain((void *)amplitudesStorage);
    g_swift_retain((void *)frequenciesStorage);
    sw_call_x0_x1_x8(g_audio_meters_init, (void *)amplitudesStorage,
                     (void *)frequenciesStorage, meters);

    typedef struct {
        double anchorX;
        double anchorY;
        float opacity;
    } AnchorOpacityArguments;
    AnchorOpacityArguments anchorOpacity = {0.5, 0.5, 1.0f};
    sw_options_init_meters(g_options_meters_init, options,
                           &g_personality_byte[personality], meters, &anchorOpacity);
    // SiriWaveOptions.init(audioMeters:) consumes the 16-byte AudioMeters.
    free(meters);

    sw_call_x20_x0_x8(g_wave, (void *)glass, options, result);
    // Probe-confirmed g_wave borrow semantics: destroy the caller's options;
    // the returned Glass has its own retains for both meter arrays.
    g_options_destroy(options, g_options_md);
    free(options);

    g_error[0] = '\0';
    return result;
}

// Apply contentEffect(.lense) after wave options and return a distinct owned value.
void *glass_apply_lense(const void *glass) {
    if (!ensure()) return NULL;
    g_error[0] = '\0';
    if (!glass) {
        snprintf(g_error, sizeof g_error, "contentEffect(.lense) received a null _Glass value");
        return NULL;
    }
    void *result = NULL;
    if (!allocate_glass(&result, "contentEffect(.lense) _Glass")) return NULL;
    uint8_t effect = 0;
    sw_call_x8(g_lense, &effect);
    sw_call_x20_x0_x8(g_effect, (void *)glass, &effect, result);
    g_error[0] = '\0';
    return result;
}

// Apply the Bool-accepting _Glass modifier into a distinct owned result.
// The caller remains responsible for destroying the input and returned value.
void *glass_excluding_shadow(const void *glass, int32_t excludesShadow) {
    if (!ensure()) return NULL;
    g_error[0] = '\0';
    if (!glass) {
        snprintf(g_error, sizeof g_error, "excludingShadow received a null _Glass value");
        return NULL;
    }
    if (excludesShadow != 0 && excludesShadow != 1) {
        snprintf(g_error, sizeof g_error, "excludingShadow must be 0 or 1");
        return NULL;
    }

    void *result = NULL;
    if (!allocate_glass(&result, "excludingShadow _Glass")) return NULL;
    sw_call_x20_w0_x8(g_excluding_shadow, (void *)glass, (uint32_t)excludesShadow, result);
    g_error[0] = '\0';
    return result;
}

void glass_destroy(void *glass) {
    (void)release_glass(glass);
}

int32_t glass_personality_byte(int32_t index) {
    if (!ensure() || index < 0 || index > 3) return -1;
    return g_personality_byte[index];
}
