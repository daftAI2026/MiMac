//go:build darwin

package textoutput

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
	"unsafe"
)

type markedMockObject struct {
	kind       string
	text       string
	units      []uint16
	key        string
	pid        int32
	rangeValue nativeRange
}

type markedMockAX struct {
	nextID         uintptr
	objects        map[uintptr]markedMockObject
	refs           map[uintptr]int
	doubleReleases []uintptr

	pid                int32
	focusedKey         string
	secureSubrole      bool
	secureEventInput   bool
	contentUnits       []uint16
	markedRanges       []nativeRange
	markedRangeIndex   int
	parameterQueries   []nativeRange
	parameterizedNames []string
	attributeReads     []string

	api *nativeAPI
}

func newMarkedMockAX(content string, ranges ...nativeRange) *markedMockAX {
	contentUnits := utf16.Encode([]rune(content))
	m := &markedMockAX{
		objects:      make(map[uintptr]markedMockObject),
		refs:         make(map[uintptr]int),
		pid:          int32(len(contentUnits) + 1), // synthetic identity; no process is queried
		focusedKey:   "original-editor",
		contentUnits: contentUnits,
		markedRanges: append([]nativeRange(nil), ranges...),
	}
	m.api = &nativeAPI{
		createApplication: func(pid int32) uintptr {
			return m.alloc(markedMockObject{kind: "application", pid: pid})
		},
		copyAttributeValue: m.copyAttributeValue,
		getPID: func(element uintptr, pid *int32) int32 {
			object, ok := m.objects[element]
			if !ok || pid == nil {
				return axErrorNoValue
			}
			*pid = object.pid
			return axErrorSuccess
		},
		cfStringCreateCString: func(_ uintptr, value *byte, _ uint32) uintptr {
			return m.alloc(markedMockObject{kind: "string", text: readMarkedMockCString(value)})
		},
		cfStringCreateBytes: func(_ uintptr, value *byte, length int64, _ uint32, _ uint8) uintptr {
			if value == nil || length < 0 {
				return 0
			}
			text := string(unsafe.Slice(value, int(length)))
			return m.alloc(markedMockObject{kind: "string", text: text, units: utf16.Encode([]rune(text))})
		},
		cfStringGetTypeID: func() uintptr { return 12 },
		cfStringGetLength: func(value uintptr) int64 {
			object, ok := m.objects[value]
			if !ok || object.kind != "string" {
				return -1
			}
			return int64(len(m.stringUnits(object)))
		},
		cfStringGetCharacters: func(value uintptr, r nativeRange, buffer *uint16) {
			object, ok := m.objects[value]
			if !ok || object.kind != "string" || buffer == nil || r.location < 0 || r.length < 0 {
				return
			}
			units := m.stringUnits(object)
			start, end := int(r.location), int(r.location+r.length)
			if end > len(units) {
				return
			}
			copy(unsafe.Slice(buffer, int(r.length)), units[start:end])
		},
		cfEqual: func(a, b uintptr) uint8 {
			left, leftOK := m.objects[a]
			right, rightOK := m.objects[b]
			if !leftOK || !rightOK || left.kind != right.kind {
				return 0
			}
			switch left.kind {
			case "element":
				if left.pid == right.pid && left.key == right.key {
					return 1
				}
			case "string":
				if left.text == right.text {
					return 1
				}
			}
			return 0
		},
		cfRelease: m.release,
		cfGetTypeID: func(value uintptr) uintptr {
			object, ok := m.objects[value]
			if !ok {
				return 0
			}
			switch object.kind {
			case "string":
				return 12
			case "axvalue":
				return 13
			default:
				return 14
			}
		},
		axValueGetTypeID: func() uintptr { return 13 },
		axValueGetType: func(value uintptr) uint32 {
			if object, ok := m.objects[value]; ok && object.kind == "axvalue" {
				return axValueCFRange
			}
			return 0
		},
		axValueGetValue: func(value uintptr, valueType uint32, output unsafe.Pointer) uint8 {
			object, ok := m.objects[value]
			if !ok || object.kind != "axvalue" || valueType != axValueCFRange || output == nil {
				return 0
			}
			*(*nativeRange)(output) = object.rangeValue
			return 1
		},
		axValueCreate: func(valueType uint32, input unsafe.Pointer) uintptr {
			if valueType != axValueCFRange || input == nil {
				return 0
			}
			r := *(*nativeRange)(input)
			return m.alloc(markedMockObject{kind: "axvalue", rangeValue: r})
		},
		copyParameterizedValue: m.copyParameterizedValue,
		isSecureEventInputOn: func() uint8 {
			if m.secureEventInput {
				return 1
			}
			return 0
		},
	}
	return m
}

func readMarkedMockCString(value *byte) string {
	if value == nil {
		return ""
	}
	var result []byte
	for offset := uintptr(0); offset < 4096; offset++ {
		b := *(*byte)(unsafe.Add(unsafe.Pointer(value), offset))
		if b == 0 {
			break
		}
		result = append(result, b)
	}
	return string(result)
}

func (m *markedMockAX) alloc(object markedMockObject) uintptr {
	m.nextID++
	id := m.nextID
	m.objects[id] = object
	m.refs[id]++
	return id
}

func (m *markedMockAX) stringUnits(object markedMockObject) []uint16 {
	if object.units != nil {
		return object.units
	}
	return utf16.Encode([]rune(object.text))
}

func (m *markedMockAX) release(value uintptr) {
	if value == 0 {
		return
	}
	if m.refs[value] <= 0 {
		m.doubleReleases = append(m.doubleReleases, value)
		return
	}
	m.refs[value]--
}

func (m *markedMockAX) copyAttributeValue(element, attribute uintptr, output *uintptr) int32 {
	if output == nil {
		return axErrorNoValue
	}
	name := m.objects[attribute].text
	m.attributeReads = append(m.attributeReads, name)
	owner := m.objects[element]
	switch {
	case owner.kind == "application" && name == "AXFocusedUIElement":
		*output = m.alloc(markedMockObject{kind: "element", pid: owner.pid, key: m.focusedKey})
		return axErrorSuccess
	case owner.kind == "element" && name == "AXSubrole" && m.secureSubrole:
		*output = m.alloc(markedMockObject{kind: "string", text: "AXSecureTextField"})
		return axErrorSuccess
	case owner.kind == "element" && name == "AXTextInputMarkedRange":
		if len(m.markedRanges) == 0 {
			return axErrorNoValue
		}
		index := m.markedRangeIndex
		if index >= len(m.markedRanges) {
			index = len(m.markedRanges) - 1
		}
		m.markedRangeIndex++
		*output = m.alloc(markedMockObject{kind: "axvalue", rangeValue: m.markedRanges[index]})
		return axErrorSuccess
	}
	*output = 0
	return axErrorNoValue
}

func (m *markedMockAX) copyParameterizedValue(element, attribute, parameter uintptr, output *uintptr) int32 {
	if output == nil {
		return axErrorNoValue
	}
	name := m.objects[attribute].text
	m.parameterizedNames = append(m.parameterizedNames, name)
	field := m.objects[element]
	argument := m.objects[parameter]
	if field.kind != "element" || field.key != "original-editor" || argument.kind != "axvalue" || name != "AXStringForRange" {
		return axErrorParamUnsupported
	}
	r := argument.rangeValue
	m.parameterQueries = append(m.parameterQueries, r)
	if r.location < 0 || r.length < 0 || r.location+r.length > int64(len(m.contentUnits)) {
		return axErrorParamUnsupported
	}
	start, end := int(r.location), int(r.location+r.length)
	units := append([]uint16(nil), m.contentUnits[start:end]...)
	text := string(utf16.Decode(units))
	*output = m.alloc(markedMockObject{kind: "string", text: text, units: units})
	return axErrorSuccess
}

func (m *markedMockAX) newField(t *testing.T, anchor int64) markedTextField {
	t.Helper()
	pid := m.pid
	element := m.alloc(markedMockObject{kind: "element", pid: pid, key: "original-editor"})
	rangeAttribute, err := m.api.makeString("AXTextInputMarkedRange")
	if err != nil {
		t.Fatal(err)
	}
	stringAttribute, err := m.api.makeString("AXStringForRange")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.api.cfRelease(stringAttribute)
		m.api.cfRelease(rangeAttribute)
		m.api.cfRelease(element)
		for id, count := range m.refs {
			if count != 0 {
				t.Errorf("native reference %d remains retained %d time(s)", id, count)
			}
		}
		if len(m.doubleReleases) != 0 {
			t.Errorf("native references released more than once: %v", m.doubleReleases)
		}
	})
	return markedTextField{
		api: m.api, frontmostPID: func() (int32, error) { return m.pid, nil },
		element: element, rangeAttribute: rangeAttribute,
		stringAttribute: stringAttribute, pid: pid, anchor: anchor,
	}
}

func markedUTF16Length(text string) int64 {
	return int64(len(utf16.Encode([]rune(text))))
}

func TestMarkedDraftReadsOnlyTheOriginalMarkedRangeWithSurroundingText(t *testing.T) {
	prefix, draft, suffix := "先前内容|", "草稿", "|后续内容"
	content := prefix + draft + suffix
	r := nativeRange{location: markedUTF16Length(prefix), length: markedUTF16Length(draft)}
	mock := newMarkedMockAX(content, r, r)
	field := mock.newField(t, r.location)

	got, stable, err := field.draft()
	if err != nil || !stable || got != draft {
		t.Fatalf("draft() = (%q, %t, %v), want (%q, true, nil)", got, stable, err, draft)
	}
	if len(mock.parameterQueries) != 1 || mock.parameterQueries[0] != r {
		t.Fatalf("AXStringForRange queries = %#v, want only marked range %#v", mock.parameterQueries, r)
	}
	if len(mock.parameterizedNames) != 1 || mock.parameterizedNames[0] != "AXStringForRange" {
		t.Fatalf("parameterized AX attributes = %#v, want only AXStringForRange", mock.parameterizedNames)
	}
	for _, name := range mock.attributeReads {
		if name == "AXValue" || name == "AXSelectedText" || name == "AXSelectedTextRange" {
			t.Fatalf("draft path read unrelated editor attribute %q", name)
		}
	}
}

func TestMarkedDraftDropsOnlyAnIncompleteUTF16TailAndKeepsEmbeddedNUL(t *testing.T) {
	t.Run("surrogate pair clipped by bounded tail", func(t *testing.T) {
		composition := "😀" + strings.Repeat("界", 255)
		r := nativeRange{location: 0, length: markedUTF16Length(composition)}
		mock := newMarkedMockAX(composition, r, r)
		field := mock.newField(t, r.location)

		got, stable, err := field.draft()
		want := strings.Repeat("界", 255)
		if err != nil || !stable || got != want {
			t.Fatalf("bounded draft = (%q, %t, %v), want intact UTF-16 tail (%q, true, nil)", got, stable, err, want)
		}
		if len(mock.parameterQueries) != 1 || mock.parameterQueries[0] != (nativeRange{location: 1, length: 256}) {
			t.Fatalf("bounded query = %#v, want tail range {1 256}", mock.parameterQueries)
		}
	})

	t.Run("embedded NUL is retained", func(t *testing.T) {
		composition := "草稿\x00后续"
		r := nativeRange{location: 0, length: markedUTF16Length(composition)}
		mock := newMarkedMockAX(composition, r, r)
		field := mock.newField(t, r.location)

		got, stable, err := field.draft()
		if err != nil || !stable || got != composition {
			t.Fatalf("embedded-NUL draft = (%q, %t, %v), want exact content (%q, true, nil)", got, stable, err, composition)
		}
	})
}

func TestMarkedDraftWaitsForAnIMERevisionBeforePublishing(t *testing.T) {
	composition := "初稿修订稿"
	start := markedUTF16Length("原有内容|")
	first := nativeRange{location: start, length: markedUTF16Length("初稿")}
	revised := nativeRange{location: start, length: markedUTF16Length("初稿修订稿")}
	mock := newMarkedMockAX("原有内容|"+composition+"|后文", first, revised)
	field := mock.newField(t, start)

	got, stable, err := field.draft()
	if err != nil || stable || got != "" {
		t.Fatalf("draft during range revision = (%q, %t, %v), want empty unstable result", got, stable, err)
	}
	mock.markedRanges = []nativeRange{revised, revised}
	mock.markedRangeIndex = 0
	got, stable, err = field.draft()
	if err != nil || !stable || got != composition {
		t.Fatalf("revised draft = (%q, %t, %v), want (%q, true, nil)", got, stable, err, composition)
	}
}

func TestMarkedDraftAbortsForSecureOrChangedFocusBeforeReadingText(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*markedMockAX)
	}{
		{name: "secure text field", change: func(mock *markedMockAX) { mock.secureSubrole = true }},
		{name: "secure event input", change: func(mock *markedMockAX) { mock.secureEventInput = true }},
		{name: "different focused element", change: func(mock *markedMockAX) { mock.focusedKey = "replacement-editor" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := nativeRange{location: 0, length: markedUTF16Length("private")}
			mock := newMarkedMockAX("private", r, r)
			field := mock.newField(t, r.location)
			test.change(mock)

			if got, stable, err := field.draft(); stable || !errors.Is(err, ErrTargetChanged) || got != "" {
				t.Fatalf("draft() = (%q, %t, %v), want empty, unstable target-change abort", got, stable, err)
			}
			for _, name := range mock.attributeReads {
				if name == "AXTextInputMarkedRange" {
					t.Fatal("composition range was queried after secure-input or focus validation failed")
				}
			}
			if len(mock.parameterQueries) != 0 {
				t.Fatalf("draft text reads after abort = %#v, want none", mock.parameterQueries)
			}
		})
	}
}

func TestMarkedDraftTreatsEmptyCompositionAsStableAndReleasesAXValues(t *testing.T) {
	r := nativeRange{location: markedUTF16Length("existing"), length: 0}
	mock := newMarkedMockAX("existing tail", r)
	field := mock.newField(t, r.location)

	got, stable, err := field.draft()
	if err != nil || !stable || got != "" {
		t.Fatalf("empty marked range = (%q, %t, %v), want (\"\", true, nil)", got, stable, err)
	}
	if len(mock.parameterQueries) != 0 {
		t.Fatalf("empty composition caused text reads: %#v", mock.parameterQueries)
	}
}

func TestRealCFStringGetCharactersABIWithSyntheticText(t *testing.T) {
	api, err := loadNativeAPI()
	if err != nil {
		t.Fatalf("load CoreFoundation API: %v", err)
	}
	text := "context|😀\x00tail"
	raw := []byte(text)
	value := api.cfStringCreateBytes(0, &raw[0], int64(len(raw)), cfStringEncodingUTF8, 0)
	if value == 0 {
		t.Fatal("CFStringCreateWithBytes returned null for synthetic content")
	}
	defer api.cfRelease(value)
	// CFStringCreateWithBytes copies this synthetic buffer before returning.

	units := api.cfStringGetLength(value)
	if want := markedUTF16Length(text); units != want {
		t.Fatalf("CFString UTF-16 length = %d, want %d", units, want)
	}
	start := markedUTF16Length("context|")
	length := units - start
	buffer := make([]uint16, length)
	api.cfStringGetCharacters(value, nativeRange{location: start, length: length}, &buffer[0])
	if got, want := string(utf16.Decode(buffer)), "😀\x00tail"; got != want {
		t.Fatalf("CFStringGetCharacters ABI returned %q, want %q", got, want)
	}

	lowSurrogate := make([]uint16, 1)
	api.cfStringGetCharacters(value, nativeRange{location: start + 1, length: 1}, &lowSurrogate[0])
	if lowSurrogate[0] < 0xDC00 || lowSurrogate[0] > 0xDFFF {
		t.Fatalf("single UTF-16 code unit at surrogate boundary = %#04x, want low surrogate", lowSurrogate[0])
	}
}
