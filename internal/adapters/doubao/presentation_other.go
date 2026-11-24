//go:build !darwin

package doubao

func captureRecordingBarBaselineNative(int32, string) *RecordingBarBaseline { return nil }
