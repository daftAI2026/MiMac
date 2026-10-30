package distributednotification

import (
	"context"
	"errors"
	"testing"
	"unsafe"
)

type fakeCoreFoundation struct {
	api        coreFoundationAPI
	next       uintptr
	strings    map[uintptr]string
	releases   map[uintptr]int
	center     uintptr
	posted     bool
	postCenter uintptr
	postName   string
	postObject string
	postInfo   uintptr
	deliver    uint8
	failString string
}

func newFakeCoreFoundation() *fakeCoreFoundation {
	fake := &fakeCoreFoundation{
		next:     100,
		strings:  make(map[uintptr]string),
		releases: make(map[uintptr]int),
		center:   42,
	}
	fake.api = coreFoundationAPI{
		cfStringCreateWithCString: func(_ uintptr, value *byte, encoding uint32) uintptr {
			if encoding != cfStringEncodingUTF8 {
				return 0
			}
			text := readFakeString(value)
			if text == fake.failString {
				return 0
			}
			fake.next++
			fake.strings[fake.next] = text
			return fake.next
		},
		cfNotificationCenterGetDistributedCenter: func() uintptr { return fake.center },
		cfNotificationCenterPostNotification: func(center, name, object, userInfo uintptr, deliver uint8) {
			fake.posted = true
			fake.postCenter = center
			fake.postName = fake.strings[name]
			fake.postObject = fake.strings[object]
			fake.postInfo = userInfo
			fake.deliver = deliver
		},
		cfRelease: func(ref uintptr) { fake.releases[ref]++ },
	}
	return fake
}

func readFakeString(value *byte) string {
	var result []byte
	start := unsafe.Pointer(value)
	for offset := uintptr(0); offset < 4096; offset++ {
		current := *(*byte)(unsafe.Add(start, offset))
		if current == 0 {
			return string(result)
		}
		result = append(result, current)
	}
	panic("unterminated CFString input")
}

func TestPostObjectUsesNilUserInfoAndImmediateDelivery(t *testing.T) {
	fake := newFakeCoreFoundation()
	if err := postObjectWithAPI(context.Background(), "test.channel", "YWJj", &fake.api); err != nil {
		t.Fatal(err)
	}
	if !fake.posted || fake.postCenter != fake.center || fake.postName != "test.channel" ||
		fake.postObject != "YWJj" || fake.postInfo != 0 || fake.deliver != 1 {
		t.Fatalf("post = %#v; want distributed center, object string, nil userInfo, immediate delivery", fake)
	}
	if len(fake.releases) != len(fake.strings) {
		t.Fatalf("released %d objects, created %d", len(fake.releases), len(fake.strings))
	}
	for ref, count := range fake.releases {
		if count != 1 {
			t.Errorf("CF object %#x released %d times, want exactly once", ref, count)
		}
	}
}

func TestPostObjectRejectsInvalidStringsBeforePosting(t *testing.T) {
	for _, test := range []struct {
		name         string
		notification string
		object       string
	}{
		{name: "empty name", notification: "", object: "value"},
		{name: "name contains NUL", notification: "test\x00channel", object: "value"},
		{name: "object contains NUL", notification: "test.channel", object: "value\x00suffix"},
		{name: "name is invalid UTF-8", notification: string([]byte{0xff}), object: "value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newFakeCoreFoundation()
			err := postObjectWithAPI(context.Background(), test.notification, test.object, &fake.api)
			if !errors.Is(err, ErrInvalidString) || fake.posted {
				t.Fatalf("error/post = %v/%v, want ErrInvalidString and no post", err, fake.posted)
			}
		})
	}
}

func TestPostObjectHonorsCancellationBeforeNativeCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fake := newFakeCoreFoundation()
	if err := postObjectWithAPI(ctx, "test.channel", "value", &fake.api); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if fake.posted || len(fake.strings) != 0 {
		t.Fatalf("native calls occurred after cancellation: %#v", fake)
	}
}

func TestPostObjectReleasesNameWhenObjectCreationFails(t *testing.T) {
	fake := newFakeCoreFoundation()
	fake.failString = "bad-object"
	if err := postObjectWithAPI(context.Background(), "test.channel", "bad-object", &fake.api); err == nil {
		t.Fatal("CFString creation failure was swallowed")
	}
	if fake.posted || len(fake.releases) != 1 {
		t.Fatalf("failed object creation state = %#v", fake)
	}
}
