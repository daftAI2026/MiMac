package bridge

import (
	"reflect"
	"testing"
)

type voiceAdmissionCase struct {
	name    string
	setup   func(*testing.T) (*Core, PressToken)
	allowed bool
	block   BlockReason
}

func TestCanBeginVoiceMatchesVoiceBeganAdmission(t *testing.T) {
	tests := []voiceAdmissionCase{
		{
			name: "allowed",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				return core, mustPress(t, device, "voice", 1)
			},
			allowed: true,
		},
		{
			name: "unconfigured",
			setup: func(t *testing.T) (*Core, PressToken) {
				core := NewUnconfiguredCore()
				device := mustDevice(t, "remote-1", 1)
				mustStep(t, core, DeviceConnected(device))
				mustStep(t, core, RouteAvailable(true))
				mustStep(t, core, ScopeAvailable(true))
				return core, mustPress(t, device, "voice", 1)
			},
		},
		{
			name: "configured after construction",
			setup: func(t *testing.T) (*Core, PressToken) {
				core := NewUnconfiguredCore()
				device := mustDevice(t, "remote-1", 1)
				mustStep(t, core, DeviceConnected(device))
				mustStep(t, core, RouteAvailable(true))
				mustStep(t, core, ScopeAvailable(true))
				mustStep(t, core, Configure(mustBinding(t, PolicyAfterDrain, "space")))
				return core, mustPress(t, device, "voice", 1)
			},
			allowed: true,
		},
		{
			name: "route unavailable",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				mustStep(t, core, RouteAvailable(false))
				return core, mustPress(t, device, "voice", 1)
			},
		},
		{
			name: "scope unavailable",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				mustStep(t, core, ScopeAvailable(false))
				return core, mustPress(t, device, "voice", 1)
			},
		},
		{
			name: "disconnected",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				mustStep(t, core, DeviceDisconnected(device))
				return core, mustPress(t, device, "voice", 1)
			},
		},
		{
			name: "old press ID",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				mustStep(t, core, VoiceEnded(mustPress(t, device, "voice", 2)))
				return core, mustPress(t, device, "voice", 1)
			},
		},
		{
			name: "active button slot",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				mustStep(t, core, ObservedBegan(mustPress(t, device, "voice", 1)))
				return core, mustPress(t, device, "voice", 2)
			},
		},
		{
			name: "duplicate began",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				press := mustPress(t, device, "voice", 1)
				if effects := mustStep(t, core, VoiceBegan(press)); !hasEffect[PrepareSourceEffect](effects) {
					t.Fatalf("initial VoiceBegan did not prepare source: %#v", effects)
				}
				return core, press
			},
		},
		{
			name: "session active",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				if effects := mustStep(t, core, VoiceBegan(mustPress(t, device, "voice", 1))); !hasEffect[PrepareSourceEffect](effects) {
					t.Fatalf("initial VoiceBegan did not prepare source: %#v", effects)
				}
				return core, mustPress(t, device, "voice-second", 2)
			},
		},
		{
			name: "action lease",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				plan := actionTestPlan(t, ActionGlobal, ActionHold, RequestStart, RequestFinish, "command")
				mustStep(t, core, TargetActionBegan(mustPress(t, device, "action", 1), plan))
				return core, mustPress(t, device, "voice", 2)
			},
			block: BlockActionConflictsWithHeldKey,
		},
		{
			name: "toggle key conflict",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyToggle, "space"))
				mustStep(t, core, OrdinaryBegan(mustPress(t, device, "ordinary", 1), "space"))
				return core, mustPress(t, device, "voice", 2)
			},
			block: BlockTapConflictsWithHeldKey,
		},
		{
			name: "toggle recovery lock",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyToggle, "ctrl", "space"))
				voice := mustPress(t, device, "voice", 1)
				mustStep(t, core, VoiceBegan(voice))
				mustStep(t, core, RouteAvailable(false))
				mustStep(t, core, VoiceEnded(voice))
				mustStep(t, core, RouteAvailable(true))
				return core, mustPress(t, device, "voice", 2)
			},
			block: BlockToggleRecoveryRequired,
		},
		{
			name: "output failure",
			setup: func(t *testing.T) (*Core, PressToken) {
				core, device := setupCore(t, mustBinding(t, PolicyAfterDrain, "space"))
				mustStep(t, core, OutputFailed(1))
				return core, mustPress(t, device, "voice", 1)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			core, press := test.setup(t)
			before := core.Snapshot()
			got := core.CanBeginVoice(press)
			if got != test.allowed {
				t.Fatalf("CanBeginVoice() = %v, want %v", got, test.allowed)
			}
			if after := core.Snapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("CanBeginVoice mutated Core: before=%#v after=%#v", before, after)
			}

			effects := mustStep(t, core, VoiceBegan(press))
			prepared := hasEffect[PrepareSourceEffect](effects)
			if prepared != got {
				t.Fatalf("CanBeginVoice()=%v but VoiceBegan effects=%#v", got, effects)
			}
			if test.block == 0 {
				if reason, ok := blockedReason(effects); ok {
					t.Fatalf("unexpected BlockedEffect reason %v in %#v", reason, effects)
				}
			} else if reason, ok := blockedReason(effects); !ok || reason != test.block {
				t.Fatalf("BlockedEffect=(%v,%v), want (%v,true); effects=%#v", reason, ok, test.block, effects)
			}
		})
	}
}

func TestCanBeginVoiceRejectsInvalidAndUninitializedCore(t *testing.T) {
	if (*Core)(nil).CanBeginVoice(PressToken{}) {
		t.Fatal("nil Core admitted a voice press")
	}
	if (&Core{}).CanBeginVoice(PressToken{}) {
		t.Fatal("uninitialized Core admitted an invalid voice press")
	}
	core := NewUnconfiguredCore()
	if core.CanBeginVoice(PressToken{}) {
		t.Fatal("Core admitted an invalid press")
	}
}
