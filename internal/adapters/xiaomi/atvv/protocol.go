package atvv

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var (
	ErrMalformedPacket    = errors.New("atvv: malformed packet")
	ErrUnsupportedVersion = errors.New("atvv: unsupported version")
	ErrUnsupportedCodec   = errors.New("atvv: unsupported codec")
	ErrInvalidStepIndex   = errors.New("atvv: invalid step index")
	ErrUnknownOpcode      = errors.New("atvv: unknown control opcode")
)

type Codec uint8

const (
	CodecADPCM8k  Codec = 0x01
	CodecADPCM16k Codec = 0x02
)

const knownCodecMask byte = 0x03

type CodecCandidate struct {
	Mask        byte
	FieldOffset uint8
}

type Capabilities struct {
	Version           uint16
	Raw               []byte
	RawField3         byte
	RawField4         byte
	DeclaredFrameSize uint16
	Tail              []byte
	CodecCandidate    CodecCandidate
}

// ParseCapabilities parses the v1 response prefix while preserving its
// complete payload and any unrecognized trailing bytes.
func ParseCapabilities(raw []byte) (Capabilities, error) {
	if len(raw) < 7 || raw[0] != 0x0B {
		return Capabilities{}, ErrMalformedPacket
	}

	version := binary.BigEndian.Uint16(raw[1:3])
	if version != 0x0100 {
		return Capabilities{}, ErrUnsupportedVersion
	}

	snapshot := bytes.Clone(raw)
	result := Capabilities{
		Version:           version,
		Raw:               snapshot,
		RawField3:         snapshot[3],
		RawField4:         snapshot[4],
		DeclaredFrameSize: binary.BigEndian.Uint16(snapshot[5:7]),
		Tail:              snapshot[7:],
	}

	if mask := snapshot[3] & knownCodecMask; mask != 0 {
		result.CodecCandidate = CodecCandidate{Mask: mask, FieldOffset: 3}
	} else if mask := snapshot[4] & knownCodecMask; mask != 0 {
		result.CodecCandidate = CodecCandidate{Mask: mask, FieldOffset: 4}
	}

	return result, nil
}

type ControlKind uint8

const (
	ControlStartSearch ControlKind = iota + 1
	ControlAudioStart
	ControlAudioSync
	ControlAudioStop
	ControlMicrophoneOpenError
)

type ControlMessage struct {
	Kind      ControlKind
	Raw       []byte
	Reason    byte
	HasReason bool
	Codec     Codec
	StreamID  byte
	Sequence  uint16
	Predictor int16
	StepIndex uint8
	ErrorCode uint16
}

// ParseControl parses the v1 control events consumed by the ATVV adapter.
// Host-to-remote commands are serialized separately and are not control events.
func ParseControl(raw []byte) (ControlMessage, error) {
	if len(raw) == 0 {
		return ControlMessage{}, ErrMalformedPacket
	}

	switch raw[0] {
	case 0x08:
		return ControlMessage{
			Kind: ControlStartSearch,
			Raw:  bytes.Clone(raw),
		}, nil

	case 0x04:
		if len(raw) < 4 {
			return ControlMessage{}, ErrMalformedPacket
		}
		codec, err := parseCodec(raw[2])
		if err != nil {
			return ControlMessage{}, err
		}
		return ControlMessage{
			Kind:      ControlAudioStart,
			Raw:       bytes.Clone(raw),
			Reason:    raw[1],
			HasReason: true,
			Codec:     codec,
			StreamID:  raw[3],
		}, nil

	case 0x0A:
		if len(raw) < 7 {
			return ControlMessage{}, ErrMalformedPacket
		}
		codec, err := parseCodec(raw[1])
		if err != nil {
			return ControlMessage{}, err
		}
		stepIndex := raw[6]
		if stepIndex > 88 {
			return ControlMessage{}, ErrInvalidStepIndex
		}
		return ControlMessage{
			Kind:      ControlAudioSync,
			Raw:       bytes.Clone(raw),
			Codec:     codec,
			Sequence:  binary.BigEndian.Uint16(raw[2:4]),
			Predictor: int16(binary.BigEndian.Uint16(raw[4:6])),
			StepIndex: stepIndex,
		}, nil

	case 0x00:
		message := ControlMessage{
			Kind: ControlAudioStop,
			Raw:  bytes.Clone(raw),
		}
		if len(raw) > 1 {
			message.Reason = raw[1]
			message.HasReason = true
		}
		return message, nil

	case 0x0C:
		if len(raw) < 3 {
			return ControlMessage{}, ErrMalformedPacket
		}
		return ControlMessage{
			Kind:      ControlMicrophoneOpenError,
			Raw:       bytes.Clone(raw),
			ErrorCode: binary.BigEndian.Uint16(raw[1:3]),
		}, nil

	default:
		return ControlMessage{}, ErrUnknownOpcode
	}
}

func parseCodec(value byte) (Codec, error) {
	codec := Codec(value)
	switch codec {
	case CodecADPCM8k, CodecADPCM16k:
		return codec, nil
	default:
		return 0, ErrUnsupportedCodec
	}
}

func MicrophoneOpen() [2]byte {
	return [2]byte{0x0C, 0x00}
}

func MicrophoneClose(streamID byte) [2]byte {
	return [2]byte{0x0D, streamID}
}

func MicrophoneExtend(streamID byte) [2]byte {
	return [2]byte{0x0E, streamID}
}
