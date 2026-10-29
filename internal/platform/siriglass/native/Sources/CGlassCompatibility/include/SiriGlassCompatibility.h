#pragma once

#include <stdint.h>

// Resolves every private SwiftUI symbol used by the derived _Glass adapter and
// validates the private _Glass, SiriWaveOptions.AudioMeters, and SiriWaveOptions
// value-witness sizes/strides and destroy slots (40/40, 16/16, and 48/48), plus
// the matching public Glass layout. It never closes the private framework image
// after loading it. Returns a readable error on any mismatch; it does not
// compose or convert a value.
int32_t SiriGlassCheckPrivateCompatibility(uint64_t swiftSize,
                                           uint64_t swiftStride,
                                           uint64_t *systemSize,
                                           uint64_t *systemStride,
                                           char *errorBuffer,
                                           int32_t errorCapacity);
