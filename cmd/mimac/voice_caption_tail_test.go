package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLongTentativeCaptionKeepsLatestRevision(t *testing.T) {
	for _, stable := range []string{"", "已经确认"} {
		tentative := strings.Repeat("临时文字", maxVoiceInterimScanBytes) + "新词🙂"
		draft, ok := boundVoiceInterim(stable, tentative)
		if !ok || !utf8.ValidString(draft.Tentative) || !strings.HasPrefix(draft.Tentative, "…") || !strings.HasSuffix(draft.Tentative, "新词🙂") {
			t.Fatalf("lost the latest words in a tentative revision: %+v", draft)
		}
		if len(draft.Stable)+len(draft.Tentative) > maxVoiceInterimBytes {
			t.Fatal("caption exceeded the session byte limit")
		}
	}
}
