package atvv

import (
	"slices"
	"testing"
)

func TestDecoderArbitraryNotificationBoundaries(t *testing.T) {
	var input [359]byte
	for i := range input {
		input[i] = byte(i*29 + 17)
	}
	var oneShot Decoder
	want := decoderTestDecodeSamples(t, &oneShot, input[:])
	for _, size := range []int{1, 2, 7, 60, 119, 120, 121, 181, 359} {
		var d Decoder
		got := make([]int16, 2*len(input))
		for offset := 0; offset < len(input); offset += size {
			end := min(offset+size, len(input))
			if _, err := d.Decode(got[2*offset:2*end], input[offset:end]); err != nil {
				t.Fatal(err)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("notification size %d changed decoded samples", size)
		}
	}
}

func TestDecoderSteadyStateDoesNotAllocate(t *testing.T) {
	var d Decoder
	var input [120]byte
	var output [240]int16
	var n int
	var err error
	for i := range input {
		input[i] = byte(i*29 + 17)
	}
	allocations := testing.AllocsPerRun(1000, func() {
		n, err = d.Decode(output[:], input[:])
	})
	if n != len(output) || err != nil || allocations != 0 {
		t.Fatalf("n=%d err=%v allocations=%g; want 240, nil, 0", n, err, allocations)
	}
}

// 120 bytes is a benchmark payload size, not a required notification boundary.
func BenchmarkDecode120Bytes(b *testing.B) {
	var d Decoder
	var input [120]byte
	var output [240]int16
	for i := range input {
		input[i] = byte(i*29 + 17)
	}
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := d.Decode(output[:], input[:]); err != nil {
			b.Fatal(err)
		}
	}
}
