//go:build !darwin

package wetype

func captureRecordingBarBaselineNative(int32, string) *RecordingBarBaseline { return nil }
