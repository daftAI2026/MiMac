package wetype

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"mimac.local/mimac/internal/voicetools"
)

func TestNewMicrophoneSelectionRequestEncodesType66Payload(t *testing.T) {
	empty := ""
	uid := "dev.mimac.audio/雪"
	for _, test := range []struct {
		name        string
		uid         *string
		wantPayload string
	}{
		{name: "nil selects automatic", wantPayload: `{"type":66,"args":""}`},
		{name: "empty selects automatic", uid: &empty, wantPayload: `{"type":66,"args":""}`},
		{name: "UID preserved", uid: &uid, wantPayload: `{"type":66,"args":"dev.mimac.audio/雪"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := NewMicrophoneSelectionRequest(test.uid)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := base64.StdEncoding.DecodeString(request.NotificationObject())
			if err != nil {
				t.Fatalf("decode Base64 object: %v", err)
			}
			if string(decoded) != test.wantPayload {
				t.Fatalf("payload = %s, want %s", decoded, test.wantPayload)
			}
		})
	}
}

func TestNewMicrophoneSelectionRequestRejectsInvalidUID(t *testing.T) {
	for _, test := range []struct {
		name string
		uid  string
	}{
		{name: "NUL", uid: "device\x00uid"},
		{name: "invalid UTF-8", uid: string([]byte{0xff})},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewMicrophoneSelectionRequest(&test.uid); !errors.Is(err, ErrInvalidMicrophoneSelection) {
				t.Fatalf("error = %v, want ErrInvalidMicrophoneSelection", err)
			}
		})
	}
}

func TestNewMicrophoneSelectionRequestBoundsUIDSize(t *testing.T) {
	tooLarge := strings.Repeat("x", maxPreferredMicrophoneUIDBytes+1)
	if _, err := NewMicrophoneSelectionRequest(&tooLarge); !errors.Is(err, ErrMicrophoneSelectionTooLarge) {
		t.Fatalf("oversized UID error = %v, want ErrMicrophoneSelectionTooLarge", err)
	}
}

func TestRequestMicrophoneSelectionVerifiesBeforePosting(t *testing.T) {
	uid := "device-uid"
	var calls []string
	var postedName, postedObject string
	err := requestMicrophoneSelection(context.Background(), voicetools.Locator{}, &uid,
		func(context.Context, voicetools.Locator) error {
			calls = append(calls, "verify")
			return nil
		},
		func(_ context.Context, name, object string) error {
			calls = append(calls, "post")
			postedName, postedObject = name, object
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "verify,post" {
		t.Fatalf("call order = %v, want verify then post", calls)
	}
	if postedName != "Settings2WeType_channel" {
		t.Fatalf("notification name = %q, want %q", postedName, "Settings2WeType_channel")
	}
	want, _ := NewMicrophoneSelectionRequest(&uid)
	if postedObject != want.NotificationObject() {
		t.Fatalf("notification object = %q, want %q", postedObject, want.NotificationObject())
	}
}

func TestRequestMicrophoneSelectionDoesNotPostAfterVerifierFailure(t *testing.T) {
	wantErr := errors.New("identity mismatch")
	posts := 0
	err := requestMicrophoneSelection(context.Background(), voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) error { return wantErr },
		func(context.Context, string, string) error {
			posts++
			return nil
		},
	)
	if !errors.Is(err, wantErr) || posts != 0 {
		t.Fatalf("error/posts = %v/%d, want wrapped verifier error and no post", err, posts)
	}
}

func TestRequestMicrophoneSelectionCancellationAfterVerificationPreventsPost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	posts := 0
	err := requestMicrophoneSelection(ctx, voicetools.Locator{}, nil,
		func(context.Context, voicetools.Locator) error {
			cancel()
			return nil
		},
		func(context.Context, string, string) error {
			posts++
			return nil
		},
	)
	if !errors.Is(err, context.Canceled) || posts != 0 {
		t.Fatalf("error/posts = %v/%d, want canceled and no post", err, posts)
	}
}
