#include <stdint.h>
#include <stddef.h>
static volatile float observed;
static volatile uint64_t fields[8];
static uint32_t bits(float value) { union {float f; uint32_t u;} v = {.f=value}; return v.u; }
uint64_t frame_fixture_field(uint32_t field) { return field < 8 ? fields[field] : 0; }
int32_t frame_fixture_json(uint64_t handle, const void *bytes, int32_t count) {
    return handle && bytes && count > 0;
}
int32_t frame_fixture_typed(uint64_t handle, uint64_t revision, int32_t visible, float power,
                            const float *amplitudes, const float *frequencies, int32_t count) {
    if (!handle || !revision || count < 0 || count > 16) return 0;
    if (count && (!amplitudes || !frequencies)) return 0;
    fields[0]=handle; fields[1]=revision; fields[2]=(uint32_t)visible;
    fields[3]=bits(power); fields[4]=(uint32_t)count;
    fields[5]=count ? bits(amplitudes[0]) : 0;
    fields[6]=count ? bits(amplitudes[count-1]) : 0;
    fields[7]=count ? bits(frequencies[count-1]) : 0;
    observed = power + visible;
    if (count) { if (!amplitudes || !frequencies) return 0; observed += amplitudes[0] + frequencies[count-1]; }
    return 1;
}
