package atvv

import "errors"

var (
	errInvalidStepIndex = errors.New("atvv decoder: step index out of range")
	errShortDestination = errors.New("atvv decoder: destination is too short")
)

// IMA/DVI ADPCM's standard 89-entry step table and nibble index adjustments.
var imaStepTable = [...]int32{
	7, 8, 9, 10, 11, 12, 13, 14,
	16, 17, 19, 21, 23, 25, 28, 31,
	34, 37, 41, 45, 50, 55, 60, 66,
	73, 80, 88, 97, 107, 118, 130, 143,
	157, 173, 190, 209, 230, 253, 279, 307,
	337, 371, 408, 449, 494, 544, 598, 658,
	724, 796, 876, 963, 1060, 1166, 1282, 1411,
	1552, 1707, 1878, 2066, 2272, 2499, 2749, 3024,
	3327, 3660, 4026, 4428, 4871, 5358, 5894, 6484,
	7132, 7845, 8630, 9493, 10442, 11487, 12635, 13899,
	15289, 16818, 18500, 20350, 22385, 24623, 27086, 29794,
	32767,
}

var imaIndexAdjust = [...]int8{
	-1, -1, -1, -1, 2, 4, 6, 8,
	-1, -1, -1, -1, 2, 4, 6, 8,
}

// Decoder keeps the IMA predictor and step index between payloads.
// Its zero value starts at predictor 0 and step index 0.
type Decoder struct {
	predictor int16
	stepIndex uint8
}

// Reset installs a predictor and standard IMA step index. Invalid indexes
// leave the decoder unchanged.
func (d *Decoder) Reset(predictor int16, stepIndex uint8) error {
	if stepIndex >= uint8(len(imaStepTable)) {
		return errInvalidStepIndex
	}
	d.predictor = predictor
	d.stepIndex = stepIndex
	return nil
}

// Decode expands each byte high nibble first into two signed PCM samples.
// It returns the number of samples written. A short destination is rejected
// before either output or decoder state is changed.
func (d *Decoder) Decode(dst []int16, src []byte) (int, error) {
	if len(src) > len(dst)/2 {
		return 0, errShortDestination
	}

	predictor := int32(d.predictor)
	stepIndex := int(d.stepIndex)
	out := 0
	for _, packed := range src {
		predictor, stepIndex = decodeIMANibble(predictor, stepIndex, int(packed>>4))
		dst[out] = int16(predictor)
		out++

		predictor, stepIndex = decodeIMANibble(predictor, stepIndex, int(packed&0x0f))
		dst[out] = int16(predictor)
		out++
	}

	d.predictor = int16(predictor)
	d.stepIndex = uint8(stepIndex)
	return out, nil
}

func decodeIMANibble(predictor int32, stepIndex, code int) (int32, int) {
	step := imaStepTable[stepIndex]
	delta := step >> 3
	if code&1 != 0 {
		delta += step >> 2
	}
	if code&2 != 0 {
		delta += step >> 1
	}
	if code&4 != 0 {
		delta += step
	}
	if code&8 != 0 {
		predictor -= delta
	} else {
		predictor += delta
	}
	if predictor > 32767 {
		predictor = 32767
	} else if predictor < -32768 {
		predictor = -32768
	}

	stepIndex += int(imaIndexAdjust[code])
	if stepIndex < 0 {
		stepIndex = 0
	} else if stepIndex >= len(imaStepTable) {
		stepIndex = len(imaStepTable) - 1
	}
	return predictor, stepIndex
}
