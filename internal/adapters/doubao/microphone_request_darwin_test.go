//go:build darwin

package doubao

import (
	"context"
	"reflect"
	"testing"
	"unsafe"
)

type fakeMicrophoneRequestCF struct {
	api          microphoneRequestAPI
	next         uintptr
	strings      map[uintptr]string
	dictionaries map[uintptr]map[string]string
	releases     map[uintptr]int
	failString   bool
	failedString string
	center       uintptr
	posted       bool
	postCenter   uintptr
	postName     string
	postObject   uintptr
	postUserInfo uintptr
	postDeliver  uint8
}

func newFakeMicrophoneRequestCF() *fakeMicrophoneRequestCF {
	fake := &fakeMicrophoneRequestCF{
		next:         100,
		strings:      make(map[uintptr]string),
		dictionaries: make(map[uintptr]map[string]string),
		releases:     make(map[uintptr]int),
		center:       42,
	}
	fake.api = microphoneRequestAPI{
		cfStringCreateWithCString: func(_ uintptr, value *byte, encoding uint32) uintptr {
			if encoding != cfStringEncodingUTF8 {
				return 0
			}
			text := readFakeCFString(value)
			if fake.failString && text == fake.failedString {
				return 0
			}
			ref := fake.nextRef()
			fake.strings[ref] = text
			return ref
		},
		cfDictionaryCreateMutable: func(_ uintptr, capacity int64, keyCallbacks, valueCallbacks uintptr) uintptr {
			if capacity != 0 || keyCallbacks != 1 || valueCallbacks != 2 {
				return 0
			}
			ref := fake.nextRef()
			fake.dictionaries[ref] = make(map[string]string)
			return ref
		},
		cfDictionarySetValue: func(dictionary, key, value uintptr) {
			fake.dictionaries[dictionary][fake.strings[key]] = fake.strings[value]
		},
		cfNotificationCenterGetDistributedCenter: func() uintptr { return fake.center },
		cfNotificationCenterPostNotification: func(center, name, object, userInfo uintptr, deliver uint8) {
			fake.posted = true
			fake.postCenter = center
			fake.postName = fake.strings[name]
			fake.postObject = object
			fake.postUserInfo = userInfo
			fake.postDeliver = deliver
		},
		cfRelease:                func(ref uintptr) { fake.releases[ref]++ },
		dictionaryKeyCallbacks:   1,
		dictionaryValueCallbacks: 2,
	}
	return fake
}

func (fake *fakeMicrophoneRequestCF) nextRef() uintptr {
	fake.next++
	return fake.next
}

func readFakeCFString(value *byte) string {
	var result []byte
	start := unsafe.Pointer(value)
	for offset := uintptr(0); offset < 1024; offset++ {
		current := *(*byte)(unsafe.Add(start, offset))
		if current == 0 {
			return string(result)
		}
		result = append(result, current)
	}
	panic("unterminated CFString input")
}

func TestPostSelectedMicrophoneIDNotificationWithNativeFake(t *testing.T) {
	empty := ""
	uid := "dev.mimac.audio.compat.v1"
	for _, test := range []struct {
		name       string
		selectedID *string
		want       map[string]string
	}{
		{name: "nil sends no userInfo", want: nil},
		{name: "empty sends explicit value", selectedID: &empty, want: map[string]string{selectedMicrophoneUserInfoKey: ""}},
		{name: "UID is preserved", selectedID: &uid, want: map[string]string{selectedMicrophoneUserInfoKey: uid}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := NewMicrophoneSelectionRequest(test.selectedID)
			if err != nil {
				t.Fatal(err)
			}
			fake := newFakeMicrophoneRequestCF()
			if err := postSelectedMicrophoneIDNotificationWithAPI(context.Background(), request, &fake.api); err != nil {
				t.Fatal(err)
			}
			if !fake.posted || fake.postCenter != fake.center || fake.postName != selectedMicrophoneNotificationName || fake.postObject != 0 || fake.postDeliver != 1 {
				t.Fatalf("post = %#v, want distributed center, object nil, deliverImmediately=true", fake)
			}
			if test.want == nil {
				if fake.postUserInfo != 0 {
					t.Fatalf("nil selection userInfo = %#x, want null", fake.postUserInfo)
				}
			} else if fake.postUserInfo == 0 || !reflect.DeepEqual(fake.dictionaries[fake.postUserInfo], test.want) {
				t.Fatalf("posted userInfo = %#v, want %#v", fake.dictionaries[fake.postUserInfo], test.want)
			}
			for ref, count := range fake.releases {
				if count != 1 {
					t.Errorf("CF object %#x released %d times, want exactly once", ref, count)
				}
			}
			if len(fake.releases) != len(fake.strings)+len(fake.dictionaries) {
				t.Fatalf("released %d refs for %d created CF objects", len(fake.releases), len(fake.strings)+len(fake.dictionaries))
			}
		})
	}
}

func TestPostSelectedMicrophoneIDNotificationWithNativeFakePropagatesCreateFailure(t *testing.T) {
	uid := "dev.mimac.audio.compat.v1"
	request, err := NewMicrophoneSelectionRequest(&uid)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeMicrophoneRequestCF()
	fake.failString = true
	fake.failedString = uid
	if err := postSelectedMicrophoneIDNotificationWithAPI(context.Background(), request, &fake.api); err == nil {
		t.Fatal("native CFString creation failure was swallowed")
	}
	if fake.posted {
		t.Fatal("notification was posted after CFString creation failed")
	}
	for ref, count := range fake.releases {
		if count != 1 {
			t.Errorf("CF object %#x released %d times, want exactly once", ref, count)
		}
	}
	if len(fake.releases) != 3 {
		t.Fatalf("released %d refs after failed value creation, want notification name, dictionary, and key", len(fake.releases))
	}
}
