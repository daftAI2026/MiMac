package atvv

import (
	"bytes"
	"errors"
	"testing"
)

func v1Capabilities(field3, field4 byte, tail ...byte) []byte {
	packet := []byte{0x0B, 0x01, 0x00, field3, field4, 0x00, 0x78}
	return append(packet, tail...)
}

func TestParseV1CapabilitiesKeepsRawFieldsAndCandidateProvenance(t *testing.T) {
	tests := []struct {
		name      string
		field3    byte
		field4    byte
		tail      []byte
		candidate CodecCandidate
	}{
		{name: "standard seven bytes", field3: 0x02, field4: 0x03,
			candidate: CodecCandidate{Mask: 0x02, FieldOffset: 3}},
		{name: "standard eight bytes", field3: 0x01, field4: 0x03, tail: []byte{0xA5},
			candidate: CodecCandidate{Mask: 0x01, FieldOffset: 3}},
		{name: "standard nine bytes", field3: 0x03, field4: 0x02, tail: []byte{0x00, 0x00},
			candidate: CodecCandidate{Mask: 0x03, FieldOffset: 3}},
		{name: "alternate seven bytes", field3: 0x00, field4: 0x03,
			candidate: CodecCandidate{Mask: 0x03, FieldOffset: 4}},
		{name: "alternate eight bytes", field3: 0x00, field4: 0x02, tail: []byte{0x5A},
			candidate: CodecCandidate{Mask: 0x02, FieldOffset: 4}},
		{name: "alternate nine bytes", field3: 0x00, field4: 0x03, tail: []byte{0x00, 0x00},
			candidate: CodecCandidate{Mask: 0x03, FieldOffset: 4}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := v1Capabilities(test.field3, test.field4, test.tail...)
			got, err := ParseCapabilities(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != 0x0100 {
				t.Fatalf("version = %#04x, want 0x0100", got.Version)
			}
			if got.RawField3 != test.field3 || got.RawField4 != test.field4 {
				t.Fatalf("raw fields = (%#02x, %#02x), want (%#02x, %#02x)",
					got.RawField3, got.RawField4, test.field3, test.field4)
			}
			if got.DeclaredFrameSize != 120 {
				t.Fatalf("declared frame size = %d, want raw value 120", got.DeclaredFrameSize)
			}
			if !bytes.Equal(got.Raw, input) {
				t.Fatalf("raw = %x, want %x", got.Raw, input)
			}
			if !bytes.Equal(got.Tail, test.tail) {
				t.Fatalf("tail = %x, want %x", got.Tail, test.tail)
			}
			if got.CodecCandidate != test.candidate {
				t.Fatalf("codec candidate = %#v, want %#v", got.CodecCandidate, test.candidate)
			}
		})
	}
}

func TestParseObservedRC003CapabilitiesAsNineRawBytes(t *testing.T) {
	input := []byte{0x0B, 0x01, 0x00, 0x00, 0x03, 0x00, 0x78, 0x00, 0x00}
	got, err := ParseCapabilities(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != 9 || !bytes.Equal(got.Raw, input) {
		t.Fatalf("observed capability raw = %x (%d bytes), want %x (9 bytes)", got.Raw, len(got.Raw), input)
	}
	if got.RawField3 != 0x00 || got.RawField4 != 0x03 {
		t.Fatalf("observed raw fields = (%#02x, %#02x), want (0x00, 0x03)", got.RawField3, got.RawField4)
	}
	if got.CodecCandidate != (CodecCandidate{Mask: 0x03, FieldOffset: 4}) {
		t.Fatalf("observed codec candidate = %#v, want mask 0x03 from byte 4", got.CodecCandidate)
	}
	if !bytes.Equal(got.Tail, []byte{0x00, 0x00}) {
		t.Fatalf("observed tail = %x, want 0000", got.Tail)
	}
}

func TestCapabilitiesWithNoCodecCandidatePreserveUnknownHighBits(t *testing.T) {
	input := []byte{0x0B, 0x01, 0x00, 0x80, 0x40, 0x00, 0x78, 0xA5}
	got, err := ParseCapabilities(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodecCandidate.Mask != 0 {
		t.Fatalf("candidate mask = %#02x, want no known codec candidate", got.CodecCandidate.Mask)
	}
	if got.RawField3 != 0x80 || got.RawField4 != 0x40 {
		t.Fatalf("raw fields = (%#02x, %#02x), want (0x80, 0x40)", got.RawField3, got.RawField4)
	}
	if !bytes.Equal(got.Raw, input) || !bytes.Equal(got.Tail, []byte{0xA5}) {
		t.Fatalf("unknown bytes were not preserved: raw=%x tail=%x", got.Raw, got.Tail)
	}
}

func TestCodecCandidateMasksKnownBitsWithoutChangingRawFields(t *testing.T) {
	input := []byte{0x0B, 0x01, 0x00, 0x82, 0xC0, 0x00, 0x78}
	got, err := ParseCapabilities(input)
	if err != nil {
		t.Fatal(err)
	}
	if got.CodecCandidate != (CodecCandidate{Mask: 0x02, FieldOffset: 3}) {
		t.Fatalf("codec candidate = %#v, want known low bits 0x02 from byte 3", got.CodecCandidate)
	}
	if got.RawField3 != 0x82 || got.RawField4 != 0xC0 || !bytes.Equal(got.Raw, input) {
		t.Fatalf("raw high bits were lost: field3=%#02x field4=%#02x raw=%x", got.RawField3, got.RawField4, got.Raw)
	}
}

func TestCapabilitiesDoNotAliasInputAndDoNotDefaultZeroFrameSize(t *testing.T) {
	input := []byte{0x0B, 0x01, 0x00, 0x00, 0x03, 0x00, 0x00, 0xA5}
	got, err := ParseCapabilities(input)
	if err != nil {
		t.Fatal(err)
	}
	input[3] = 0x02
	input[7] = 0x00
	if got.RawField3 != 0x00 || got.RawField4 != 0x03 {
		t.Fatalf("parsed raw fields changed with input: (%#02x, %#02x)", got.RawField3, got.RawField4)
	}
	if !bytes.Equal(got.Raw, []byte{0x0B, 0x01, 0x00, 0x00, 0x03, 0x00, 0x00, 0xA5}) {
		t.Fatalf("parsed raw aliases input: %x", got.Raw)
	}
	if !bytes.Equal(got.Tail, []byte{0xA5}) {
		t.Fatalf("parsed tail aliases input: %x", got.Tail)
	}
	got.Raw[7] = 0x5A
	if !bytes.Equal(got.Tail, []byte{0x5A}) {
		t.Fatalf("tail is not a view into its owned raw copy: %x", got.Tail)
	}
	if input[7] != 0x00 {
		t.Fatalf("owned raw copy aliases input: input tail changed to %#02x", input[7])
	}
	if got.DeclaredFrameSize != 0 {
		t.Fatalf("zero frame-size field was defaulted to %d", got.DeclaredFrameSize)
	}
}

func TestParseCapabilitiesRejectsMalformedAndNonV1Packets(t *testing.T) {
	for _, input := range [][]byte{
		nil,
		{0x0B, 0x01, 0x00, 0x02, 0x03, 0x00},
		{0x00, 0x01, 0x00, 0x02, 0x03, 0x00, 0x78},
	} {
		if _, err := ParseCapabilities(input); !errors.Is(err, ErrMalformedPacket) {
			t.Fatalf("ParseCapabilities(%x) error = %v, want ErrMalformedPacket", input, err)
		}
	}
	if _, err := ParseCapabilities([]byte{0x0B, 0x00, 0x04, 0x00, 0x01, 0x00, 0x86, 0x00, 0x14}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("v0.4 capability error = %v, want ErrUnsupportedVersion", err)
	}
}

func TestParseControlSearchStartAndAudioStartFields(t *testing.T) {
	search, err := ParseControl([]byte{0x08})
	if err != nil {
		t.Fatal(err)
	}
	if search.Kind != ControlStartSearch {
		t.Fatalf("search kind = %v, want ControlStartSearch", search.Kind)
	}

	startInput := []byte{0x04, 0x7A, byte(CodecADPCM16k), 0x5C, 0xA5}
	start, err := ParseControl(startInput)
	if err != nil {
		t.Fatal(err)
	}
	if start.Kind != ControlAudioStart || !start.HasReason || start.Reason != 0x7A || start.Codec != CodecADPCM16k || start.StreamID != 0x5C {
		t.Fatalf("audio start = %#v, want reason=0x7a codec=ADPCM16k streamID=0x5c", start)
	}
	startInput[0] = 0xFF
	if !bytes.Equal(start.Raw, []byte{0x04, 0x7A, byte(CodecADPCM16k), 0x5C, 0xA5}) {
		t.Fatalf("audio-start raw aliases input or dropped unknown tail: %x", start.Raw)
	}
}

func TestParseControlRejectsTruncatedAndUnknownCodecAudioStart(t *testing.T) {
	for _, input := range [][]byte{
		nil,
		{0x04},
		{0x04, 0x01},
		{0x04, 0x01, byte(CodecADPCM16k)},
	} {
		if _, err := ParseControl(input); !errors.Is(err, ErrMalformedPacket) {
			t.Fatalf("ParseControl(%x) error = %v, want ErrMalformedPacket", input, err)
		}
	}
	if _, err := ParseControl([]byte{0x04, 0x01, 0x03, 0x09}); !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("unknown audio-start codec error = %v, want ErrUnsupportedCodec", err)
	}
}

func TestParseAudioSyncUsesBigEndianSequenceSignedPredictorAndBoundedStep(t *testing.T) {
	for _, step := range []byte{0, 88} {
		got, err := ParseControl([]byte{0x0A, byte(CodecADPCM8k), 0x12, 0x34, 0xFF, 0xFE, step})
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != ControlAudioSync || got.Codec != CodecADPCM8k || got.Sequence != 0x1234 || got.Predictor != -2 || got.StepIndex != step {
			t.Fatalf("sync = %#v, want codec=ADPCM8k sequence=0x1234 predictor=-2 step=%d", got, step)
		}
	}
}

func TestParseAudioSyncRejectsTruncationUnknownCodecAndInvalidStep(t *testing.T) {
	if _, err := ParseControl([]byte{0x0A, byte(CodecADPCM16k), 0x00, 0x01, 0x00, 0x02}); !errors.Is(err, ErrMalformedPacket) {
		t.Fatalf("truncated sync error = %v, want ErrMalformedPacket", err)
	}
	if _, err := ParseControl([]byte{0x0A, 0x03, 0x00, 0x01, 0x00, 0x02, 0x10}); !errors.Is(err, ErrUnsupportedCodec) {
		t.Fatalf("unknown sync codec error = %v, want ErrUnsupportedCodec", err)
	}
	if _, err := ParseControl([]byte{0x0A, byte(CodecADPCM16k), 0x00, 0x01, 0x00, 0x02, 89}); !errors.Is(err, ErrInvalidStepIndex) {
		t.Fatalf("invalid step-index error = %v, want ErrInvalidStepIndex", err)
	}
}

func TestParseMicrophoneOpenErrorResponse(t *testing.T) {
	got, err := ParseControl([]byte{0x0C, 0x12, 0x34})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ControlMicrophoneOpenError || got.ErrorCode != 0x1234 {
		t.Fatalf("MIC_OPEN error response = %#v, want code 0x1234", got)
	}
	hostOpen := MicrophoneOpen()
	if _, err := ParseControl(hostOpen[:]); !errors.Is(err, ErrMalformedPacket) {
		t.Fatalf("host MIC_OPEN parsed as a device error response: err=%v, want ErrMalformedPacket", err)
	}
	for _, input := range [][]byte{{0x0C}, {0x0C, 0x12}} {
		if _, err := ParseControl(input); !errors.Is(err, ErrMalformedPacket) {
			t.Fatalf("truncated MIC_OPEN error %x returned err=%v, want ErrMalformedPacket", input, err)
		}
	}
}

func TestParseAudioStopIsDistinctFromHostMicrophoneClose(t *testing.T) {
	stop, err := ParseControl([]byte{0x00, 0xA5})
	if err != nil {
		t.Fatal(err)
	}
	if stop.Kind != ControlAudioStop || !stop.HasReason || stop.Reason != 0xA5 {
		t.Fatalf("stop = %#v, want remote AudioStop reason 0xa5", stop)
	}
	micClose := MicrophoneClose(0xA5)
	if _, err := ParseControl(micClose[:]); !errors.Is(err, ErrUnknownOpcode) {
		t.Fatalf("host MIC_CLOSE parsed as remote control: err=%v, want ErrUnknownOpcode", err)
	}
	stopWithoutReason, err := ParseControl([]byte{0x00})
	if err != nil {
		t.Fatal(err)
	}
	if stopWithoutReason.Kind != ControlAudioStop || stopWithoutReason.HasReason {
		t.Fatalf("reasonless stop = %#v, want ControlAudioStop without reason", stopWithoutReason)
	}
}

func TestMicrophoneCommandsUseV1WireBytes(t *testing.T) {
	if got := MicrophoneOpen(); !bytes.Equal(got[:], []byte{0x0C, 0x00}) {
		t.Fatalf("MIC_OPEN = %x, want 0c00", got)
	}
	if got := MicrophoneClose(0x5C); !bytes.Equal(got[:], []byte{0x0D, 0x5C}) {
		t.Fatalf("MIC_CLOSE = %x, want 0d5c", got)
	}
	if got := MicrophoneExtend(0x5C); !bytes.Equal(got[:], []byte{0x0E, 0x5C}) {
		t.Fatalf("MIC_EXTEND = %x, want 0e5c", got)
	}
}

func TestParseControlRejectsUnknownOpcode(t *testing.T) {
	if _, err := ParseControl([]byte{0xFF}); !errors.Is(err, ErrUnknownOpcode) {
		t.Fatalf("unknown opcode error = %v, want ErrUnknownOpcode", err)
	}
}
