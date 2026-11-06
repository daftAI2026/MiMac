package doubao

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"howett.net/plist"
)

const (
	controlSettingsTestID1 = "11111111-1111-4111-8111-111111111111"
	controlSettingsTestID2 = "22222222-2222-4222-8222-222222222222"
)

type fakeControlSettingsTransport struct {
	mu           sync.Mutex
	session      *controlSettingsQuerySession
	subscription *fakeControlSettingsSubscription
	postIDs      []string
	onPost       func(*controlSettingsQuerySession)
	subscribeErr error
	postErr      error
}

type fakeControlSettingsSubscription struct {
	mu     sync.Mutex
	closed []string
}

func (fake *fakeControlSettingsTransport) subscribe(session *controlSettingsQuerySession) (controlSettingsSubscription, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.session = session
	fake.subscription = &fakeControlSettingsSubscription{}
	return fake.subscription, fake.subscribeErr
}

func (fake *fakeControlSettingsTransport) postRequest(session *controlSettingsQuerySession) error {
	fake.mu.Lock()
	fake.postIDs = append(fake.postIDs, session.requestID)
	onPost := fake.onPost
	err := fake.postErr
	fake.mu.Unlock()
	if err == nil && onPost != nil {
		onPost(session)
	}
	return err
}

func (subscription *fakeControlSettingsSubscription) removeObserver() {
	subscription.mu.Lock()
	subscription.closed = append(subscription.closed, "remove")
	subscription.mu.Unlock()
}

func (subscription *fakeControlSettingsSubscription) release() {
	subscription.mu.Lock()
	subscription.closed = append(subscription.closed, "release")
	subscription.mu.Unlock()
}

func (fake *fakeControlSettingsTransport) currentSession() *controlSettingsQuerySession {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.session
}

func (fake *fakeControlSettingsTransport) currentSubscription() *fakeControlSettingsSubscription {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.subscription
}

func controlSettingsTestPayload(t *testing.T, status string) []byte {
	t.Helper()
	root := map[string]any{"status": status}
	if status == "ok" {
		root["unknownTopLevel"] = "ignored"
		root["state"] = map[string]any{
			"voiceShortcutEnabled": true,
			"globalVoiceEnabled":   false,
			"unknownStateField":    []any{"ignored", int64(7)},
			"singleClick": map[string]any{
				"configured":    true,
				"keyCode":       int64(61),
				"modifierFlags": int64(0),
				"unknown":       "ignored",
			},
			"longPress": map[string]any{
				"configured":    false,
				"keyCode":       int64(-1),
				"modifierFlags": int64(1048592),
				"unknown":       "ignored",
			},
		}
	}
	data, err := plist.Marshal(root, plist.BinaryFormat)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func expectedControlSettings() ControlSettings {
	return ControlSettings{
		VoiceShortcutEnabled: true,
		GlobalVoiceEnabled:   false,
		SingleClick: ControlShortcutSettings{
			Configured: true, KeyCode: 61, ModifierFlags: 0,
		},
		LongPress: ControlShortcutSettings{
			Configured: false, KeyCode: -1, ModifierFlags: 1048592,
		},
	}
}

func fixedControlSettingsID(id string) func() (string, error) {
	return func() (string, error) { return id, nil }
}

func boundedControlSettingsContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestDecodeControlSettingsPayloadStrictSchemaAndUnknownFields(t *testing.T) {
	got, err := decodeControlSettingsPayload(controlSettingsTestPayload(t, "ok"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expectedControlSettings()) {
		t.Fatalf("decoded settings = %#v, want %#v", got, expectedControlSettings())
	}
}

func TestDecodeControlSettingsPayloadNotReadyAndMalformed(t *testing.T) {
	got, err := decodeControlSettingsPayload(controlSettingsTestPayload(t, "notReady"))
	if !errors.Is(err, ErrControlSettingsNotReady) {
		t.Fatalf("notReady error = %v, want ErrControlSettingsNotReady", err)
	}
	if got != (ControlSettings{}) {
		t.Fatalf("notReady settings = %#v, want zero value", got)
	}

	for _, payload := range [][]byte{
		{},
		[]byte("not a property list"),
		make([]byte, maxControlSettingsPropertyListSize+1),
	} {
		if _, err := decodeControlSettingsPayload(payload); !errors.Is(err, ErrMalformedControlSettings) {
			t.Errorf("payload length %d error = %v, want ErrMalformedControlSettings", len(payload), err)
		}
	}
}

func TestDecodeControlSettingsPayloadRejectsWrongTypes(t *testing.T) {
	base := map[string]any{
		"status": "ok",
		"state": map[string]any{
			"voiceShortcutEnabled": true,
			"globalVoiceEnabled":   false,
			"singleClick":          map[string]any{"configured": true, "keyCode": int64(61), "modifierFlags": int64(0)},
			"longPress":            map[string]any{"configured": false, "keyCode": int64(-1), "modifierFlags": int64(0)},
		},
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "status is not a string", mutate: func(root map[string]any) { root["status"] = true }},
		{name: "boolean cannot be integer", mutate: func(root map[string]any) { root["state"].(map[string]any)["voiceShortcutEnabled"] = int64(1) }},
		{name: "integer cannot be boolean", mutate: func(root map[string]any) {
			root["state"].(map[string]any)["singleClick"].(map[string]any)["keyCode"] = true
		}},
		{name: "fraction cannot be integer", mutate: func(root map[string]any) {
			root["state"].(map[string]any)["longPress"].(map[string]any)["modifierFlags"] = 1.5
		}},
		{name: "configured must be boolean", mutate: func(root map[string]any) {
			root["state"].(map[string]any)["longPress"].(map[string]any)["configured"] = "false"
		}},
		{name: "missing modifier flags", mutate: func(root map[string]any) {
			delete(root["state"].(map[string]any)["singleClick"].(map[string]any), "modifierFlags")
		}},
		{name: "unknown status", mutate: func(root map[string]any) { root["status"] = "starting" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := cloneControlSettingsMap(base)
			test.mutate(root)
			payload, err := plist.Marshal(root, plist.BinaryFormat)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeControlSettingsPayload(payload); !errors.Is(err, ErrMalformedControlSettings) {
				t.Fatalf("decode error = %v, want ErrMalformedControlSettings", err)
			}
		})
	}
}

func cloneControlSettingsMap(value map[string]any) map[string]any {
	return cloneControlSettingsValue(value).(map[string]any)
}

func cloneControlSettingsValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, field := range typed {
			result[key] = cloneControlSettingsValue(field)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, field := range typed {
			result[index] = cloneControlSettingsValue(field)
		}
		return result
	default:
		return value
	}
}

func TestNewControlSettingsRequestIDIsCanonicalUUIDv4(t *testing.T) {
	first, err := newControlSettingsRequestID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newControlSettingsRequestID()
	if err != nil {
		t.Fatal(err)
	}
	if !canonicalUUID(first) || !canonicalUUID(second) || first == second {
		t.Fatalf("UUIDs = %q and %q, want unique canonical UUIDs", first, second)
	}
	if first[14] != '4' || (first[19] != '8' && first[19] != '9' && first[19] != 'a' && first[19] != 'b') {
		t.Fatalf("UUID %q does not use RFC 4122 v4/variant bits", first)
	}
}

func TestQueryControlSettingsCorrelatesOneUUIDAndIgnoresUnknownFields(t *testing.T) {
	fake := &fakeControlSettingsTransport{}
	payload := controlSettingsTestPayload(t, "ok")
	fake.onPost = func(session *controlSettingsQuerySession) {
		receiveControlSettingsPayload(session, controlSettingsTestID2, []byte("malformed wrong-UUID reply"))
		receiveControlSettingsPayload(session, session.requestID, payload)
		receiveControlSettingsPayload(session, session.requestID, []byte("duplicate reply ignored"))
	}
	got, err := queryControlSettings(boundedControlSettingsContext(t), fake, fixedControlSettingsID(controlSettingsTestID1))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expectedControlSettings()) {
		t.Fatalf("settings = %#v, want %#v", got, expectedControlSettings())
	}
	if !reflect.DeepEqual(fake.postIDs, []string{controlSettingsTestID1}) {
		t.Fatalf("posted IDs = %#v, want one UUID", fake.postIDs)
	}
	if got := fake.currentSubscription().closed; !reflect.DeepEqual(got, []string{"remove", "release"}) {
		t.Fatalf("subscription cleanup = %#v, want remove then release", got)
	}
}

func TestQueryControlSettingsReturnsExplicitNotReady(t *testing.T) {
	fake := &fakeControlSettingsTransport{}
	fake.onPost = func(session *controlSettingsQuerySession) {
		receiveControlSettingsPayload(session, session.requestID, controlSettingsTestPayload(t, "notReady"))
	}
	if _, err := queryControlSettings(boundedControlSettingsContext(t), fake, fixedControlSettingsID(controlSettingsTestID1)); !errors.Is(err, ErrControlSettingsNotReady) {
		t.Fatalf("query error = %v, want ErrControlSettingsNotReady", err)
	}
}

func TestQueryControlSettingsPropagatesMalformedCorrelatedReply(t *testing.T) {
	fake := &fakeControlSettingsTransport{}
	fake.onPost = func(session *controlSettingsQuerySession) {
		receiveControlSettingsPayload(session, session.requestID, []byte("malformed"))
	}
	if _, err := queryControlSettings(boundedControlSettingsContext(t), fake, fixedControlSettingsID(controlSettingsTestID1)); !errors.Is(err, ErrMalformedControlSettings) {
		t.Fatalf("query error = %v, want ErrMalformedControlSettings", err)
	}
}

func TestQueryControlSettingsCancellationRemovesObserverAndIgnoresLateReply(t *testing.T) {
	fake := &fakeControlSettingsTransport{}
	posted := make(chan *controlSettingsQuerySession, 1)
	fake.onPost = func(session *controlSettingsQuerySession) { posted <- session }
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := queryControlSettings(ctx, fake, fixedControlSettingsID(controlSettingsTestID1))
		result <- err
	}()
	session := <-posted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("query error = %v, want context.Canceled", err)
	}
	receiveControlSettingsPayload(session, session.requestID, controlSettingsTestPayload(t, "ok"))
	if got := fake.currentSubscription().closed; !reflect.DeepEqual(got, []string{"remove", "release"}) {
		t.Fatalf("subscription cleanup = %#v, want remove then release", got)
	}
	controlSettingsSessionMu.Lock()
	active := activeControlSettingsSession
	controlSettingsSessionMu.Unlock()
	if active != nil {
		t.Fatalf("late reply left active session %#v", active)
	}
}

func TestQueryControlSettingsSingleFlightAndReopenIgnoreLateReply(t *testing.T) {
	fake := &fakeControlSettingsTransport{}
	posted := make(chan *controlSettingsQuerySession, 2)
	fake.onPost = func(session *controlSettingsQuerySession) { posted <- session }

	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() {
		_, err := queryControlSettings(firstContext, fake, fixedControlSettingsID(controlSettingsTestID1))
		firstResult <- err
	}()
	firstSession := <-posted

	waitingContext, cancelWaiting := context.WithCancel(context.Background())
	waitingResult := make(chan error, 1)
	go func() {
		_, err := queryControlSettings(waitingContext, fake, fixedControlSettingsID(controlSettingsTestID2))
		waitingResult <- err
	}()
	cancelWaiting()
	if err := <-waitingResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("second in-flight query error = %v, want context.Canceled", err)
	}
	if len(fake.postIDs) != 1 {
		t.Fatalf("posts during first query = %#v, want one", fake.postIDs)
	}
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first query error = %v, want context.Canceled", err)
	}

	secondContext := boundedControlSettingsContext(t)
	secondResult := make(chan error, 1)
	go func() {
		settings, err := queryControlSettings(secondContext, fake, fixedControlSettingsID(controlSettingsTestID2))
		if err == nil && !reflect.DeepEqual(settings, expectedControlSettings()) {
			err = errors.New("reopened query returned unexpected settings")
		}
		secondResult <- err
	}()
	secondSession := <-posted
	if secondSession == firstSession {
		t.Fatal("reopened query reused the prior session")
	}
	receiveControlSettingsPayload(firstSession, firstSession.requestID, controlSettingsTestPayload(t, "ok"))
	if len(secondSession.result) != 0 {
		t.Fatal("late reply from the prior query entered the reopened session")
	}
	receiveControlSettingsPayload(secondSession, secondSession.requestID, controlSettingsTestPayload(t, "ok"))
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.postIDs, []string{controlSettingsTestID1, controlSettingsTestID2}) {
		t.Fatalf("query posts = %#v, want one per sequential query", fake.postIDs)
	}
}

func TestQueryControlSettingsObserverAndPostFailuresCleanUp(t *testing.T) {
	for _, test := range []struct {
		name string
		fake func() *fakeControlSettingsTransport
	}{
		{name: "observer registration", fake: func() *fakeControlSettingsTransport {
			return &fakeControlSettingsTransport{subscribeErr: errors.New("register failed")}
		}},
		{name: "request post", fake: func() *fakeControlSettingsTransport {
			return &fakeControlSettingsTransport{postErr: errors.New("post failed")}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := test.fake()
			_, err := queryControlSettings(boundedControlSettingsContext(t), fake, fixedControlSettingsID(controlSettingsTestID1))
			if err == nil {
				t.Fatal("failure was swallowed")
			}
			if subscription := fake.currentSubscription(); subscription != nil {
				if got := subscription.closed; !reflect.DeepEqual(got, []string{"remove", "release"}) {
					t.Fatalf("subscription cleanup = %#v, want remove then release", got)
				}
			}
		})
	}
}

func TestPlistSignedIntegerRejectsOverflowAndFloat(t *testing.T) {
	if _, ok := plistSignedInteger(uint64(math.MaxInt64) + 1); ok {
		t.Fatal("uint64 overflow was accepted")
	}
	if _, ok := plistSignedInteger(float64(1)); ok {
		t.Fatal("float was accepted as a plist integer")
	}
}
