package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"mimac.local/mimac/internal/bridge"
)

func voiceInterimTestToken(t *testing.T, generation uint64) bridge.SessionToken {
	t.Helper()
	binding, err := bridge.NewBinding(bridge.PolicyAfterDrain, "Control", "Space")
	if err != nil {
		t.Fatal(err)
	}
	core, err := bridge.NewCore(binding)
	if err != nil {
		t.Fatal(err)
	}
	device, err := bridge.NewDevice("voice-interim-test", generation)
	if err != nil {
		t.Fatal(err)
	}
	press, err := bridge.NewPressToken(device, "voice", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []bridge.Event{bridge.DeviceConnected(device), bridge.RouteAvailable(true), bridge.ScopeAvailable(true), bridge.VoiceBegan(press)} {
		if _, err := core.Step(event); err != nil {
			t.Fatal(err)
		}
	}
	token := core.Snapshot().Session.Token
	if token == (bridge.SessionToken{}) {
		t.Fatal("Core did not issue a test session token")
	}
	return token
}

func TestVoiceInterimFeedBoundsLatestTextToTheActiveSession(t *testing.T) {
	var feed voiceInterimFeed
	first := voiceInterimTestToken(t, 1)
	second := voiceInterimTestToken(t, 2) // Same numeric ID; different device generation.
	if first.ID() != second.ID() || first == second {
		t.Fatal("test tokens did not isolate the device generation")
	}
	feed.SetEnabled(true)
	feed.Begin(first)
	if !feed.Publish(first, "  first\nsecond", "  tentative\tline  ") {
		t.Fatal("active session interim was rejected")
	}
	if got := feed.Snapshot(first); got.Stable != "  first second" || got.Tentative != "  tentative line  " {
		t.Fatalf("interim = %+v, want preserved whitespace on one line", got)
	}

	long := strings.Repeat("你好", maxVoiceInterimBytes)
	if !feed.Publish(first, long, "") {
		t.Fatal("bounded active interim was rejected")
	}
	got := feed.Snapshot(first)
	if len(got.Stable)+len(got.Tentative) > maxVoiceInterimBytes || !utf8.ValidString(got.Stable) || !strings.HasPrefix(got.Stable, "…") || !strings.HasSuffix(got.Stable, "你好") {
		t.Fatalf("bounded interim lost its newest stable words: %+v", got)
	}
	longCJK := strings.Repeat("起始", maxVoiceInterimScanBytes) + "最新词组终点"
	draft, valid := boundVoiceInterim(longCJK, "")
	if !valid || !strings.HasPrefix(draft.Stable, "…") || !strings.HasSuffix(draft.Stable, "最新词组终点") {
		t.Fatalf("bounded CJK interim lost its newest words: %+v", draft)
	}
	if feed.Publish(second, "late old text", "") {
		t.Fatal("stale session publication was accepted")
	}
	if feed.Publish(first, string([]byte{0xff}), "") {
		t.Fatal("invalid UTF-8 interim was accepted")
	}
	if feed.Snapshot(first) != got {
		t.Fatal("rejected publication replaced the current draft")
	}

	feed.Begin(second)
	if feed.Snapshot(first) != (voiceInterimDraft{}) || feed.Snapshot(second) != (voiceInterimDraft{}) {
		t.Fatal("a new session retained prior interim text")
	}
	if !feed.Publish(second, "new session", "") {
		t.Fatal("new session interim was rejected")
	}
	feed.End(first)
	if feed.Snapshot(second).Stable != "new session" {
		t.Fatal("late old-session end cleared the current draft")
	}
	feed.End(second)
	if feed.Snapshot(second) != (voiceInterimDraft{}) || feed.Publish(second, "after end", "") {
		t.Fatal("retired session accepted or retained interim text")
	}
}

func TestVoiceInterimFeedDisableDropsAndRejectsDrafts(t *testing.T) {
	var feed voiceInterimFeed
	token := voiceInterimTestToken(t, 1)
	feed.SetEnabled(true)
	feed.Begin(token)
	feed.Publish(token, "draft", "")
	feed.SetEnabled(false)
	if got := feed.Snapshot(token); got != (voiceInterimDraft{}) {
		t.Fatalf("disabled feed retained %q", got)
	}
	feed.Begin(token)
	if feed.Publish(token, "after disable", "") || feed.Snapshot(token) != (voiceInterimDraft{}) {
		t.Fatal("disabled feed accepted or retained a callback")
	}
}
