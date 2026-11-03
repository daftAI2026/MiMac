#include <stdint.h>
#include <stddef.h>
static volatile float observed;
int32_t frame_fixture_json(uint64_t handle, const void *bytes, int32_t count) {
    return handle && bytes && count > 0;
}
int32_t frame_fixture_typed(uint64_t handle, uint64_t revision, int32_t visible, float power,
                            const float *amplitudes, const float *frequencies, int32_t count) {
    if (!handle || !revision || count < 0 || count > 16) return 0;
    observed = power + visible;
    if (count) { if (!amplitudes || !frequencies) return 0; observed += amplitudes[0] + frequencies[count-1]; }
    return 1;
}
