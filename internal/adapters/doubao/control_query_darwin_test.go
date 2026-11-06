//go:build darwin

package doubao

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

type fakeControlSettingsCFObject struct {
	typeID uintptr
	text   string
	data   []byte
}

type fakeControlSettingsCF struct {
	mu                  sync.Mutex
	next                uintptr
	center              uintptr
	stringTypeID        uintptr
	dictionaryTypeID    uintptr
	dataTypeID          uintptr
	objects             map[uintptr]*fakeControlSettingsCFObject
	releases            map[uintptr]int
	events              []string
	failString          string
	propertyListFailure bool
	addCenter           uintptr
	addObserver         uintptr
	addCallback         uintptr
	addName             uintptr
	addObjectRef        uintptr
	addBehavior         int64
	removeCenter        uintptr
	removeObserver      uintptr
	removeName          uintptr
	removeObject        uintptr
	postCenter          uintptr
	postName            uintptr
	postObject          uintptr
	postUserInfo        uintptr
	postDeliver         uint8
	api                 *controlSettingsNativeAPI
}

func newFakeControlSettingsCF() *fakeControlSettingsCF {
	fake := &fakeControlSettingsCF{
		next:             100,
		center:           42,
		stringTypeID:     1,
		dictionaryTypeID: 2,
		dataTypeID:       3,
		objects:          make(map[uintptr]*fakeControlSettingsCFObject),
		releases:         make(map[uintptr]int),
	}
	base := &microphoneRequestAPI{
		cfStringCreateWithCString: func(_ uintptr, value *byte, encoding uint32) uintptr {
			if encoding != cfStringEncodingUTF8 {
				return 0
			}
			text := readFakeCFString(value)
			if fake.failString == text {
				return 0
			}
			return fake.addObject(fake.stringTypeID, text, nil)
		},
		cfNotificationCenterGetDistributedCenter: func() uintptr { return fake.center },
		cfNotificationCenterPostNotification: func(center, name, object, userInfo uintptr, deliver uint8) {
			fake.mu.Lock()
			fake.postCenter, fake.postName, fake.postObject = center, name, object
			fake.postUserInfo, fake.postDeliver = userInfo, deliver
			fake.events = append(fake.events, "post")
			fake.mu.Unlock()
		},
		cfRelease: func(ref uintptr) {
			fake.mu.Lock()
			fake.releases[ref]++
			if object := fake.objects[ref]; object != nil && object.typeID == fake.dataTypeID {
				fake.events = append(fake.events, "release:data")
			} else if object != nil {
				fake.events = append(fake.events, "release:"+object.text)
			} else {
				fake.events = append(fake.events, "release:data")
			}
			fake.mu.Unlock()
		},
	}
	fake.api = &controlSettingsNativeAPI{
		base:     base,
		callback: 77,
		cfGetTypeID: func(ref uintptr) uintptr {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if object := fake.objects[ref]; object != nil {
				return object.typeID
			}
			return 0
		},
		cfStringGetTypeID: func() uintptr { return fake.stringTypeID },
		cfStringGetLength: func(ref uintptr) int64 {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if object := fake.objects[ref]; object != nil && object.typeID == fake.stringTypeID {
				return int64(len(object.text))
			}
			return -1
		},
		cfStringGetCString: func(ref uintptr, buffer *byte, size int64, encoding uint32) uint8 {
			if encoding != cfStringEncodingUTF8 || size <= 0 {
				return 0
			}
			fake.mu.Lock()
			object := fake.objects[ref]
			text := ""
			if object != nil && object.typeID == fake.stringTypeID {
				text = object.text
			}
			fake.mu.Unlock()
			if object == nil || len(text)+1 > int(size) {
				return 0
			}
			bytes := unsafe.Slice(buffer, int(size))
			copy(bytes, text)
			bytes[len(text)] = 0
			return 1
		},
		cfDictionaryGetTypeID: func() uintptr { return fake.dictionaryTypeID },
		cfPropertyListCreateData: func(_ uintptr, value uintptr, format int64, options uint64, errorOut *uintptr) uintptr {
			if format != int64(cfPropertyListBinaryFormatV1) || options != 0 || errorOut != nil {
				return 0
			}
			fake.mu.Lock()
			defer fake.mu.Unlock()
			object := fake.objects[value]
			if fake.propertyListFailure || object == nil || object.typeID != fake.dictionaryTypeID {
				return 0
			}
			return fake.addObjectLocked(fake.dataTypeID, "", append([]byte(nil), object.data...))
		},
		cfDataGetLength: func(ref uintptr) int64 {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if object := fake.objects[ref]; object != nil && object.typeID == fake.dataTypeID {
				return int64(len(object.data))
			}
			return -1
		},
		cfDataGetBytePtr: func(ref uintptr) *byte {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if object := fake.objects[ref]; object != nil && object.typeID == fake.dataTypeID && len(object.data) > 0 {
				return &object.data[0]
			}
			return nil
		},
		cfNotificationCenterAddObserver: func(center, observer, callback, name, object uintptr, behavior int64) {
			fake.mu.Lock()
			fake.addCenter, fake.addObserver, fake.addCallback = center, observer, callback
			fake.addName, fake.addObjectRef, fake.addBehavior = name, object, behavior
			fake.events = append(fake.events, "add")
			fake.mu.Unlock()
		},
		cfNotificationCenterRemoveObserver: func(center, observer, name, object uintptr) {
			fake.mu.Lock()
			fake.removeCenter, fake.removeObserver = center, observer
			fake.removeName, fake.removeObject = name, object
			fake.events = append(fake.events, "remove")
			fake.mu.Unlock()
		},
	}
	return fake
}

func (fake *fakeControlSettingsCF) addObject(typeID uintptr, text string, data []byte) uintptr {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.addObjectLocked(typeID, text, data)
}

func (fake *fakeControlSettingsCF) addObjectLocked(typeID uintptr, text string, data []byte) uintptr {
	fake.next++
	ref := fake.next
	fake.objects[ref] = &fakeControlSettingsCFObject{typeID: typeID, text: text, data: data}
	return ref
}

func (fake *fakeControlSettingsCF) string(value string) uintptr {
	return fake.addObject(fake.stringTypeID, value, nil)
}

func (fake *fakeControlSettingsCF) dictionary(payload []byte) uintptr {
	return fake.addObject(fake.dictionaryTypeID, "userInfo", append([]byte(nil), payload...))
}

func (fake *fakeControlSettingsCF) objectText(ref uintptr) string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if object := fake.objects[ref]; object != nil {
		return object.text
	}
	return ""
}

func (fake *fakeControlSettingsCF) objectCount(typeID uintptr) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	count := 0
	for _, object := range fake.objects {
		if object.typeID == typeID {
			count++
		}
	}
	return count
}

func activateControlSettingsTestSession(t *testing.T, session *controlSettingsQuerySession) {
	t.Helper()
	controlSettingsSessionMu.Lock()
	if activeControlSettingsSession != nil {
		controlSettingsSessionMu.Unlock()
		t.Fatal("another control settings query session is active")
	}
	activeControlSettingsSession = session
	controlSettingsSessionMu.Unlock()
	t.Cleanup(func() {
		deactivateControlSettingsSession(session)
		clearControlSettingsSession(session)
	})
}

func TestDarwinControlSettingsNativeQueryUsesCorrelatedObserverAndReleasesRefs(t *testing.T) {
	fake := newFakeControlSettingsCF()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session := &controlSettingsQuerySession{
		ctx:       ctx,
		requestID: controlSettingsTestID1,
		result:    make(chan controlSettingsResult, 1),
		accepting: true,
	}
	activateControlSettingsTestSession(t, session)

	subscription, err := subscribeControlSettingsWithAPI(fake.api, session)
	if err != nil {
		t.Fatal(err)
	}
	nativeSub := subscription.(*darwinControlSettingsSubscription)
	if fake.addCenter != fake.center || fake.addObserver != nativeSub.observer || fake.addCallback == 0 ||
		fake.objectText(fake.addName) != voiceStateResponseNotificationName || fake.objectText(fake.addObjectRef) != controlSettingsTestID1 ||
		fake.addBehavior != cfNotificationDeliverImmediately {
		t.Fatalf("observer registration = %#v, does not match the exact correlated response", fake)
	}

	if err := (darwinControlSettingsTransport{}).postRequest(session); err != nil {
		t.Fatal(err)
	}
	if fake.postCenter != fake.center || fake.objectText(fake.postName) != voiceStateRequestNotificationName ||
		fake.objectText(fake.postObject) != controlSettingsTestID1 || fake.postUserInfo != 0 || fake.postDeliver != 1 {
		t.Fatalf("request post = %#v, want UUID object, nil userInfo, deliverImmediately=true", fake)
	}

	wrongObject := fake.string(controlSettingsTestID2)
	dataRefCount := fake.objectCount(fake.dataTypeID)
	handleControlSettingsNativeNotification(fake.api, nativeSub.observer, nativeSub.responseName, wrongObject, fake.dictionary(controlSettingsTestPayload(t, "ok")))
	if len(session.result) != 0 || fake.objectCount(fake.dataTypeID) != dataRefCount {
		t.Fatal("wrong UUID was not ignored before plist serialization")
	}
	userInfo := fake.dictionary(controlSettingsTestPayload(t, "ok"))
	handleControlSettingsNativeNotification(fake.api, nativeSub.observer, nativeSub.responseName, nativeSub.object, userInfo)
	result := <-session.result
	if result.err != nil || !reflect.DeepEqual(result.settings, expectedControlSettings()) {
		t.Fatalf("native result = %#v, want reviewed settings", result)
	}

	observerObject, responseName := nativeSub.object, nativeSub.responseName
	deactivateControlSettingsSession(session)
	subscription.removeObserver()
	clearControlSettingsSession(session)
	subscription.release()
	if fake.removeCenter != fake.center || fake.removeObserver != nativeSub.observer || fake.removeName != responseName || fake.removeObject != observerObject {
		t.Fatalf("observer removal = %#v, does not match registration", fake)
	}
	if fake.releases[observerObject] != 1 || fake.releases[responseName] != 1 {
		t.Fatalf("query CF refs released object=%d name=%d, want once each", fake.releases[observerObject], fake.releases[responseName])
	}
	for ref, object := range fake.objects {
		if object.typeID == fake.dataTypeID && fake.releases[ref] != 1 {
			t.Errorf("serialized CFData ref %#x released %d times, want exactly once", ref, fake.releases[ref])
		}
	}
	removeIndex, lastReferenceRelease := -1, -1
	for index, event := range fake.events {
		if event == "remove" {
			removeIndex = index
		}
		if strings.HasPrefix(event, "release:") && (strings.Contains(event, controlSettingsTestID1) || strings.Contains(event, voiceStateResponseNotificationName)) {
			lastReferenceRelease = index
		}
	}
	if removeIndex < 0 || lastReferenceRelease < removeIndex {
		t.Fatalf("observer removal/ref release order = %#v, want remove before releasing observer strings", fake.events)
	}
}

func TestDarwinControlSettingsNativeDecodeErrorsReleaseCFData(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		fail bool
	}{
		{name: "malformed plist", data: []byte("bad")},
		{name: "oversized plist", data: []byte(strings.Repeat("x", maxControlSettingsPropertyListSize+1))},
		{name: "serialization failure", fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeControlSettingsCF()
			fake.propertyListFailure = test.fail
			userInfo := fake.dictionary(test.data)
			_, err := fake.api.decodeUserInfo(userInfo)
			if !errors.Is(err, ErrMalformedControlSettings) {
				t.Fatalf("decode error = %v, want ErrMalformedControlSettings", err)
			}
			for ref, object := range fake.objects {
				if object.typeID == fake.dataTypeID && fake.releases[ref] != 1 {
					t.Errorf("CFData ref %#x released %d times, want exactly once", ref, fake.releases[ref])
				}
			}
		})
	}
}

func TestDarwinControlSettingsTeardownWaitsForCallbackCFDataRelease(t *testing.T) {
	fake := newFakeControlSettingsCF()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session := &controlSettingsQuerySession{
		ctx:       ctx,
		requestID: controlSettingsTestID1,
		result:    make(chan controlSettingsResult, 1),
		accepting: true,
	}
	activateControlSettingsTestSession(t, session)
	subscription, err := subscribeControlSettingsWithAPI(fake.api, session)
	if err != nil {
		t.Fatal(err)
	}
	nativeSub := subscription.(*darwinControlSettingsSubscription)

	enteredDataRead := make(chan struct{})
	continueDataRead := make(chan struct{})
	getLength := fake.api.cfDataGetLength
	fake.api.cfDataGetLength = func(ref uintptr) int64 {
		close(enteredDataRead)
		<-continueDataRead
		return getLength(ref)
	}
	userInfo := fake.dictionary(controlSettingsTestPayload(t, "ok"))
	callbackDone := make(chan struct{})
	go func() {
		handleControlSettingsNativeNotification(
			fake.api,
			nativeSub.observer,
			nativeSub.responseName,
			nativeSub.object,
			userInfo,
		)
		close(callbackDone)
	}()
	<-enteredDataRead

	cleanupDone := make(chan struct{})
	go func() {
		deactivateControlSettingsSession(session)
		subscription.removeObserver()
		clearControlSettingsSession(session)
		subscription.release()
		close(cleanupDone)
	}()
	close(continueDataRead)
	<-callbackDone
	<-cleanupDone

	dataReleaseIndex, removeIndex := -1, -1
	for index, event := range fake.events {
		if event == "release:data" {
			dataReleaseIndex = index
		}
		if event == "remove" {
			removeIndex = index
		}
	}
	if dataReleaseIndex < 0 || removeIndex < 0 || dataReleaseIndex > removeIndex {
		t.Fatalf("callback/teardown order = %#v, want CFData release before observer removal", fake.events)
	}
}

func TestDarwinControlSettingsPartialObserverSetupReleasesCreatedName(t *testing.T) {
	fake := newFakeControlSettingsCF()
	fake.failString = controlSettingsTestID1
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session := &controlSettingsQuerySession{
		ctx:       ctx,
		requestID: controlSettingsTestID1,
		result:    make(chan controlSettingsResult, 1),
		accepting: true,
	}
	activateControlSettingsTestSession(t, session)

	subscription, err := subscribeControlSettingsWithAPI(fake.api, session)
	if err == nil || subscription == nil {
		t.Fatalf("partial subscribe = %#v, %v; want an owned partial subscription and error", subscription, err)
	}
	deactivateControlSettingsSession(session)
	subscription.removeObserver()
	clearControlSettingsSession(session)
	subscription.release()
	for ref, object := range fake.objects {
		if object.typeID == fake.stringTypeID && fake.releases[ref] != 1 {
			t.Errorf("partial setup string ref %#x (%q) released %d times, want once", ref, object.text, fake.releases[ref])
		}
	}
	if fake.addObserver != 0 {
		t.Fatal("observer was registered after object creation failed")
	}
}

func TestDarwinControlSettingsCanceledSessionDoesNotPost(t *testing.T) {
	fake := newFakeControlSettingsCF()
	ctx, cancel := context.WithCancel(context.Background())
	session := &controlSettingsQuerySession{ctx: ctx, requestID: controlSettingsTestID1, result: make(chan controlSettingsResult, 1), accepting: true}
	activateControlSettingsTestSession(t, session)
	sub, err := subscribeControlSettingsWithAPI(fake.api, session)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		deactivateControlSettingsSession(session)
		sub.removeObserver()
		clearControlSettingsSession(session)
		sub.release()
	}()
	cancel()
	if err := (darwinControlSettingsTransport{}).postRequest(session); !errors.Is(err, context.Canceled) {
		t.Fatalf("post canceled session: %v", err)
	}
	if fake.postCenter != 0 {
		t.Fatal("canceled query reached native post")
	}
}
