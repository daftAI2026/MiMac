package doubao

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestRequestMicrophoneSelectionPayloadsWithFakePoster(t *testing.T) {
	empty := ""
	uid := "dev.mimac.audio.compat.v1"
	for _, test := range []struct {
		name       string
		selectedID *string
		want       map[string]string
	}{
		{name: "nil", want: nil},
		{name: "empty", selectedID: &empty, want: map[string]string{selectedMicrophoneUserInfoKey: ""}},
		{name: "UID", selectedID: &uid, want: map[string]string{selectedMicrophoneUserInfoKey: uid}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			var got map[string]string
			verify := func(context.Context, voicetools.Locator) error {
				calls = append(calls, "verify")
				if test.selectedID != nil {
					*test.selectedID = "caller-mutated-after-build"
				}
				return nil
			}
			post := func(_ context.Context, request MicrophoneSelectionRequest) error {
				calls = append(calls, "post")
				got = request.UserInfo()
				return nil
			}

			if err := requestMicrophoneSelection(context.Background(), voicetools.Locator{}, test.selectedID, verify, post); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, []string{"verify", "post"}) {
				t.Fatalf("call order = %v, want verify before post", calls)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("userInfo = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestRequestMicrophoneSelectionDoesNotSendWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	verifyCalled := false
	postCalled := false
	err := requestMicrophoneSelection(ctx, voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) error {
			verifyCalled = true
			return nil
		},
		func(context.Context, MicrophoneSelectionRequest) error {
			postCalled = true
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v, want context.Canceled", err)
	}
	if verifyCalled || postCalled {
		t.Fatalf("canceled request called verifier=%v poster=%v", verifyCalled, postCalled)
	}
}

func TestRequestMicrophoneSelectionHonorsCancellationAfterValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	postCalled := false
	err := requestMicrophoneSelection(ctx, voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) error {
			cancel()
			return nil
		},
		func(context.Context, MicrophoneSelectionRequest) error {
			postCalled = true
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("request error = %v, want context.Canceled", err)
	}
	if postCalled {
		t.Fatal("poster called after context was canceled by verifier")
	}
}

func TestRequestMicrophoneSelectionPropagatesTargetAndPostErrors(t *testing.T) {
	targetErr := errors.New("target validation failed")
	postCalled := false
	err := requestMicrophoneSelection(context.Background(), voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) error { return targetErr },
		func(context.Context, MicrophoneSelectionRequest) error {
			postCalled = true
			return nil
		},
	)
	if !errors.Is(err, targetErr) {
		t.Fatalf("target error = %v, want wrapped validation error", err)
	}
	if postCalled {
		t.Fatal("poster called after target validation failed")
	}

	postErr := errors.New("native post failed")
	err = requestMicrophoneSelection(context.Background(), voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) error { return nil },
		func(context.Context, MicrophoneSelectionRequest) error { return postErr },
	)
	if !errors.Is(err, postErr) {
		t.Fatalf("post error = %v, want wrapped native error", err)
	}
}

func TestNewMicrophoneSelectionRequestRejectsInvalidUTF8AndNUL(t *testing.T) {
	for _, value := range []string{"a\x00b", string([]byte{0xff})} {
		if _, err := NewMicrophoneSelectionRequest(&value); !errors.Is(err, ErrInvalidMicrophoneSelectionRequest) {
			t.Fatalf("request for %q error = %v, want ErrInvalidMicrophoneSelectionRequest", value, err)
		}
	}
}

func TestVerifyMicrophoneRequestTargetRejectsUnverifiedVersion(t *testing.T) {
	requireDarwin(t)
	locator, _ := doubaoFixtureLocator(t, bundleIdentifier, "1.0.3", verifiedBuildVersion)
	if err := verifyMicrophoneRequestTarget(context.Background(), locator); !errors.Is(err, ErrMicrophoneSelectionUnsupported) {
		t.Fatalf("target validation error = %v, want ErrMicrophoneSelectionUnsupported", err)
	}
}
