package atvv

import (
	"encoding/json"
	"os"
	"testing"
)

func decoderTestDecodeSamples(t *testing.T, d *Decoder, src []byte) []int16 {
	t.Helper()
	dst := make([]int16, 2*len(src))
	n, err := d.Decode(dst, src)
	if err != nil {
		t.Fatalf("Decode(% x): %v", src, err)
	}
	if n != len(dst) {
		t.Fatalf("Decode(% x) wrote %d samples, want %d", src, n, len(dst))
	}
	return dst
}

func decoderTestAssertSamples(t *testing.T, got, want []int16) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d samples %v, want %d samples %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d (all samples: %v)", i, got[i], want[i], got)
		}
	}
}

func TestDecoderZeroValueUsesHighNibbleFirst(t *testing.T) {
	var d Decoder
	got := decoderTestDecodeSamples(t, &d, []byte{0x70})
	decoderTestAssertSamples(t, got, []int16{11, 13})
}

func TestDecoderEmptyInputDoesNotChangeState(t *testing.T) {
	var d Decoder
	if err := d.Reset(0, 61); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	dst := []int16{111, 222}
	n, err := d.Decode(dst, nil)
	if err != nil {
		t.Fatalf("Decode empty input: %v", err)
	}
	if n != 0 {
		t.Fatalf("empty Decode wrote %d samples, want 0", n)
	}
	decoderTestAssertSamples(t, dst, []int16{111, 222})
	got := decoderTestDecodeSamples(t, &d, []byte{0x00})
	decoderTestAssertSamples(t, got, []int16{312, 596})
}

func TestDecoderLeavesExtraDestinationUntouched(t *testing.T) {
	var d Decoder
	dst := []int16{111, 222, 333}
	n, err := d.Decode(dst, []byte{0x70})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if n != 2 {
		t.Fatalf("Decode wrote %d samples, want 2", n)
	}
	decoderTestAssertSamples(t, dst, []int16{11, 13, 333})
}

func TestDecoderKnownVectorAtIndex61(t *testing.T) {
	var d Decoder
	if err := d.Reset(0, 61); err != nil {
		t.Fatalf("Reset(0, 61): %v", err)
	}
	got := decoderTestDecodeSamples(t, &d, []byte{0x00})
	decoderTestAssertSamples(t, got, []int16{312, 596})
}

// These fixed outputs use the independently sourced standard table. A low
// initial index alone cannot detect mistakes in the upper half of that table.
func TestDecoderStandardTableGoldenSamples(t *testing.T) {
	raw, err := os.ReadFile("testdata/standard-ima-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Samples [][2]int16 `json:"samples_by_initial_step_index"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Samples) != 89 {
		t.Fatalf("golden vector count = %d, want 89", len(fixture.Samples))
	}
	for index, want := range fixture.Samples {
		var d Decoder
		if err := d.Reset(-32768, uint8(index)); err != nil {
			t.Fatal(err)
		}
		got := decoderTestDecodeSamples(t, &d, []byte{0x70})
		if got[0] != want[0] || got[1] != want[1] {
			t.Errorf("initial index %d: got %v, want %v", index, got, want)
		}
	}
}

func TestDecoderSaturatesBothPredictorLimits(t *testing.T) {
	t.Run("positive", func(t *testing.T) {
		var d Decoder
		if err := d.Reset(32760, 88); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		got := decoderTestDecodeSamples(t, &d, []byte{0x77})
		decoderTestAssertSamples(t, got, []int16{32767, 32767})
	})
	t.Run("negative", func(t *testing.T) {
		var d Decoder
		if err := d.Reset(-32760, 88); err != nil {
			t.Fatalf("Reset: %v", err)
		}
		got := decoderTestDecodeSamples(t, &d, []byte{0xFF})
		decoderTestAssertSamples(t, got, []int16{-32768, -32768})
	})
}

func TestDecoderRejectsInvalidResetWithoutChangingState(t *testing.T) {
	var d Decoder
	if err := d.Reset(-1234, 61); err != nil {
		t.Fatalf("initial Reset: %v", err)
	}
	if err := d.Reset(1234, 89); err == nil {
		t.Fatal("Reset accepted step index 89, want error")
	}
	got := decoderTestDecodeSamples(t, &d, []byte{0x00})
	decoderTestAssertSamples(t, got, []int16{-922, -638})
}

func TestDecoderShortDestinationDoesNotWriteOrAdvance(t *testing.T) {
	var d Decoder
	if err := d.Reset(0, 61); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	dst := []int16{111, 222, 333}
	n, err := d.Decode(dst, []byte{0x00, 0x70})
	if err == nil {
		t.Fatal("Decode with short destination succeeded, want error")
	}
	if n != 0 {
		t.Fatalf("short Decode wrote %d samples, want 0", n)
	}
	decoderTestAssertSamples(t, dst, []int16{111, 222, 333})
	got := decoderTestDecodeSamples(t, &d, []byte{0x00})
	decoderTestAssertSamples(t, got, []int16{312, 596})
}

func TestDecoderChunkedInputMatchesOneShot(t *testing.T) {
	src := []byte{0x70, 0xAF, 0x13, 0x88, 0x00}
	var oneShot Decoder
	want := decoderTestDecodeSamples(t, &oneShot, src)

	var chunked Decoder
	var got []int16
	for _, b := range src {
		got = append(got, decoderTestDecodeSamples(t, &chunked, []byte{b})...)
	}
	decoderTestAssertSamples(t, got, want)
}

func TestDecoderShortTailDecodesImmediatelyAndKeepsState(t *testing.T) {
	src := []byte{0x70, 0x00, 0x88, 0x7F, 0x00}
	var oneShot Decoder
	want := decoderTestDecodeSamples(t, &oneShot, src)

	var chunked Decoder
	first := make([]int16, 6)
	n, err := chunked.Decode(first, src[:3])
	if err != nil {
		t.Fatalf("Decode short first chunk: %v", err)
	}
	if n != 6 {
		t.Fatalf("short first chunk wrote %d samples, want 6 immediately", n)
	}
	second := make([]int16, 4)
	n, err = chunked.Decode(second, src[3:])
	if err != nil {
		t.Fatalf("Decode short trailing chunk: %v", err)
	}
	if n != 4 {
		t.Fatalf("short trailing chunk wrote %d samples, want 4", n)
	}
	got := append(first, second...)
	decoderTestAssertSamples(t, got, want)
}
