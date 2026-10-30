package inputsource

import (
	"errors"
	"strings"
	"testing"
)

type testSource struct {
	ref uintptr
	id  string
}

func TestSelectEnabledSourceMatchesExactID(t *testing.T) {
	const requested = "com.bytedance.inputmethod.doubaoime.pinyin"
	enabled := []testSource{
		{ref: 1, id: "com.bytedance.inputmethod.doubaoime"},
		{ref: 2, id: requested},
		{ref: 3, id: requested + ".candidate"},
	}
	var selected uintptr
	err := selectExactEnabledSource(requested, len(enabled), func(index int) (uintptr, string, error) {
		return enabled[index].ref, enabled[index].id, nil
	}, func(ref uintptr) error {
		selected = ref
		return nil
	})
	if err != nil || selected != 2 {
		t.Fatalf("selected ref=%d err=%v, want exact enabled ref 2", selected, err)
	}
}

func TestSelectEnabledSourceRejectsMissingAndEmptyID(t *testing.T) {
	enabled := []testSource{{ref: 1, id: "com.bytedance.inputmethod.doubaoime.pinyin-extra"}}
	selectCalls := 0
	selectSource := func(uintptr) error { selectCalls++; return nil }
	lookup := func(index int) (uintptr, string, error) {
		return enabled[index].ref, enabled[index].id, nil
	}
	if err := selectExactEnabledSource("com.bytedance.inputmethod.doubaoime.pinyin", len(enabled), lookup, selectSource); !errors.Is(err, errInputSourceNotEnabled) {
		t.Fatalf("missing enabled source error = %v", err)
	}
	if err := selectExactEnabledSource("", len(enabled), lookup, selectSource); !errors.Is(err, errEmptyInputSourceID) {
		t.Fatalf("empty ID error = %v", err)
	}
	if selectCalls != 0 {
		t.Fatalf("selected an unlisted source %d times", selectCalls)
	}
}

func TestSelectEnabledListUsesEnabledOnlyListAndExactMatch(t *testing.T) {
	const requested = "com.bytedance.inputmethod.doubaoime.pinyin"
	enabled := map[int64]testSource{
		0: {ref: 1, id: "com.bytedance.inputmethod.doubaoime"},
		1: {ref: 2, id: requested},
		2: {ref: 3, id: requested + ".candidate"},
	}
	var properties uintptr = 1
	var includeAll uint8 = 1
	var selected uintptr
	released := 0
	err := selectEnabledFromList(requested, enabledSourceListAPI{
		createList: func(gotProperties uintptr, gotIncludeAll uint8) uintptr {
			properties, includeAll = gotProperties, gotIncludeAll
			return 44
		},
		arrayCount: func(list uintptr) int64 {
			if list != 44 {
				t.Fatalf("enumerated list ref=%d, want 44", list)
			}
			return int64(len(enabled))
		},
		arrayValue: func(list uintptr, index int64) uintptr { return enabled[index].ref },
		sourceID: func(ref uintptr) (string, error) {
			for _, source := range enabled {
				if source.ref == ref {
					return source.id, nil
				}
			}
			return "", errors.New("unknown source")
		},
		selectSource: func(ref uintptr) error { selected = ref; return nil },
		release: func(ref uintptr) {
			if ref == 44 {
				released++
			}
		},
	})
	if err != nil || selected != 2 || properties != 0 || includeAll != 0 || released != 1 {
		t.Fatalf("select ref=%d properties=%d includeAll=%d released=%d err=%v", selected, properties, includeAll, released, err)
	}
}

func TestSelectEnabledListReportsUnavailableAndNativeErrors(t *testing.T) {
	base := enabledSourceListAPI{
		createList: func(uintptr, uint8) uintptr { return 44 },
		arrayCount: func(uintptr) int64 { return 1 },
		arrayValue: func(uintptr, int64) uintptr { return 7 },
		sourceID:   func(uintptr) (string, error) { return "other", nil },
		selectSource: func(uintptr) error {
			return errors.New("should not select a different ID")
		},
		release: func(uintptr) {},
	}
	if err := selectEnabledFromList("target", base); !errors.Is(err, errInputSourceNotEnabled) {
		t.Fatalf("missing enabled source error = %v", err)
	}

	listErr := selectEnabledFromList("target", enabledSourceListAPI{
		createList:   func(uintptr, uint8) uintptr { return 0 },
		arrayCount:   func(uintptr) int64 { return 0 },
		arrayValue:   func(uintptr, int64) uintptr { return 0 },
		sourceID:     func(uintptr) (string, error) { return "", nil },
		selectSource: func(uintptr) error { return nil },
		release:      func(uintptr) {},
	})
	if listErr == nil || !strings.Contains(listErr.Error(), "could not enumerate") {
		t.Fatalf("list enumeration error = %v", listErr)
	}

	lookupErr := errors.New("input source property failed")
	base.sourceID = func(uintptr) (string, error) { return "", lookupErr }
	if err := selectEnabledFromList("target", base); !errors.Is(err, lookupErr) {
		t.Fatalf("source property error = %v", err)
	}

	selectionErr := errors.New("TISSelectInputSource failed")
	base.sourceID = func(uintptr) (string, error) { return "target", nil }
	base.selectSource = func(uintptr) error { return selectionErr }
	if err := selectEnabledFromList("target", base); !errors.Is(err, selectionErr) {
		t.Fatalf("native selection error = %v", err)
	}
}
