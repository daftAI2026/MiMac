package audiosession

import (
	"math"
	"time"

	"mimac.local/mimac/internal/bridge"
)

// InputLevel describes the latest source block observed by MiMac, not a
// receiver acknowledgement or speech result. Values are linear amplitudes in
// [0, 1]. ObservedAt is the worker's read time, not a hardware capture timestamp.
// No samples or history are retained. Consumers choose their own visual decay.
type InputLevel struct {
	RMS        float32   `json:"rms"`
	Peak       float32   `json:"peak"`
	Samples    int       `json:"samples"`
	ObservedAt time.Time `json:"observed_at"`
}

// InputLevel never prepares or reads a source. The complete token must match
// the current live resource; silence (ok=true, RMS=0) differs from no data.
func (r *Runtime) InputLevel(token bridge.SessionToken) (InputLevel, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.active
	if r.closed || s == nil || s.token != token || s.finished || s.ctx.Err() != nil || s.inputLevel.Samples == 0 {
		return InputLevel{}, false
	}
	return s.inputLevel, true
}

func measureInputLevel(pcm []int16, observedAt time.Time) InputLevel {
	if len(pcm) == 0 {
		return InputLevel{}
	}
	var squares uint64
	var peak int64
	for _, sample := range pcm {
		value := int64(sample) // Widen before abs: -32768 is valid full scale.
		squares += uint64(value * value)
		if value < 0 {
			value = -value
		}
		peak = max(peak, value)
	}
	return InputLevel{
		RMS:  float32(math.Sqrt(float64(squares)/float64(len(pcm))) / 32768),
		Peak: float32(peak) / 32768, Samples: len(pcm), ObservedAt: observedAt,
	}
}

func (r *Runtime) recordInputLevel(s *resource, pcm []int16) {
	level := measureInputLevel(pcm, time.Now())
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed && r.active == s && !s.finished && s.ctx.Err() == nil {
		s.inputLevel = level
	}
}

func (r *Runtime) clearInputLevel(s *resource) {
	r.mu.Lock()
	s.inputLevel = InputLevel{}
	r.mu.Unlock()
}
