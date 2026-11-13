package main

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"mimac.local/mimac/internal/adapters/xiaomi"
	"mimac.local/mimac/internal/voicetools"
)

func TestOnboardingPagesFitTheMinimumWindow(t *testing.T) {
	for _, dark := range []bool{false, true} {
		for step := onboardingStepDevice; step < onboardingStepCount; step++ {
			t.Run(fmt.Sprintf("step%d-dark%t", step, dark), func(t *testing.T) {
				s := &shell{development: true, settingsState: settingsState{navigationSymbols: map[symbolKey]*ui.Bitmap{}, voiceToolIcons: map[string]*voiceToolIcon{}}}
				profile, err := xiaomi.DeviceProfile()
				if err != nil {
					t.Fatal(err)
				}
				s.deviceProfiles = []devicePlugin{{ModuleID: xiaomiPlugin, Profile: profile}}
				s.onboarding.step = step
				s.onboarding.toolLocations = map[string]voicetools.State{}
				s.tools = builtInVoiceTools()
				for _, tool := range s.tools {
					s.voiceToolIcons[tool.id] = &voiceToolIcon{}
					s.onboarding.toolLocations[tool.id] = voicetools.Ready
				}
				for _, id := range []permissionGuideID{permissionGuideInput, permissionGuidePostEvent, permissionGuideBluetooth} {
					row := permissionGuideRowFor(id)
					row.access = permissionGuideNotDetermined
					s.onboarding.permissionSnapshot.rows = append(s.onboarding.permissionSnapshot.rows, row)
				}
				primeOnboardingTestSymbols(s, s.onboarding.permissionSnapshot.rows)
				v := ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
				v.SetDark(dark)
				heading, ok := v.Find(onboardingStepHeading(step))
				if !ok || math.Abs(float64(heading.X+heading.W/2-450)) > 1 {
					t.Fatalf("heading is not centered: %v %t", heading, ok)
				}
				next := "继续"
				if step == onboardingStepPermissions {
					next = "开始使用"
				}
				rect, ok := v.Find(next)
				if !ok || rect.Y < 48 || rect.Y+rect.H > 620 {
					t.Fatalf("next action outside content: %v %t", rect, ok)
				}
				if back, found := v.Find("上一步"); !found || back.W != navigationHeight || back.H != navigationHeight {
					t.Fatalf("previous action is not the compact round button: %v %t", back, found)
				}
				progress, ok := v.Find(onboardingStepPosition(step))
				if !ok || progress.Y+progress.H > 660 {
					t.Fatalf("progress outside window: %v %t", progress, ok)
				}
				if step == onboardingStepVoice {
					if _, found := v.Find("Typeless"); found {
						t.Fatal("unfinished adapters appeared in the first-use guide")
					}
					for _, name := range []string{"Apple 语音识别", "豆包输入法", "微信输入法"} {
						if r, found := v.Find(name); !found || r.X < 150 || r.Y < 48 || r.Y+r.H > rect.Y {
							t.Fatalf("tool clipped: %s %v %t", name, r, found)
						}
					}
				}
				if step == onboardingStepDevice {
					for _, title := range []string{"首次连接", "已经配对"} {
						if r, found := v.Find(title); !found || r.X < 150 || r.Y < 48 || r.Y+r.H > rect.Y {
							t.Fatalf("connection guide clipped: %s %v %t", title, r, found)
						}
					}
				}
				if step == onboardingStepPermissions {
					if _, found := v.Find("已授权 0/3"); !found {
						t.Fatal("permission summary does not report the current incomplete rows")
					}
					if _, found := v.Find("授权…"); !found {
						t.Fatal("incomplete permissions lost their existing guide action")
					}
				}
				if out := os.Getenv("MIMAC_ONBOARDING_UI_EVIDENCE"); out != "" {
					if err := os.MkdirAll(out, 0700); err != nil {
						t.Fatal(err)
					}
					f, err := os.Create(filepath.Join(out, fmt.Sprintf("step%d-dark%t.png", step, dark)))
					if err != nil {
						t.Fatal(err)
					}
					if err := png.Encode(f, v.Image()); err != nil {
						f.Close()
						t.Fatal(err)
					}
					if err := f.Close(); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestSummarizeOnboardingPermissionsOnlyCountsGrantedRows(t *testing.T) {
	tests := []struct {
		name      string
		accesses  []permissionGuideAccess
		wantCount int
		wantTotal int
		wantAll   bool
		wantLabel string
	}{
		{name: "empty"},
		{name: "all granted", accesses: []permissionGuideAccess{permissionGuideGranted, permissionGuideGranted}, wantCount: 2, wantTotal: 2, wantAll: true},
		{name: "partial with unknown and denied", accesses: []permissionGuideAccess{permissionGuideGranted, permissionGuideUnknown, permissionGuideDenied}, wantCount: 1, wantTotal: 3, wantLabel: "已授权 1/3"},
		{name: "restricted is not granted", accesses: []permissionGuideAccess{permissionGuideRestricted}, wantTotal: 1, wantLabel: "已授权 0/1"},
		{name: "not determined", accesses: []permissionGuideAccess{permissionGuideNotDetermined}, wantTotal: 1, wantLabel: "已授权 0/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := make([]permissionGuideRow, 0, len(tt.accesses))
			for _, access := range tt.accesses {
				rows = append(rows, permissionGuideRow{access: access})
			}
			got := summarizeOnboardingPermissions(rows)
			if got.granted != tt.wantCount || got.total != tt.wantTotal || got.allGranted() != tt.wantAll || got.label() != tt.wantLabel {
				t.Fatalf("summary = %+v, all=%v, label=%q; want %d/%d, all=%v, label=%q", got, got.allGranted(), got.label(), tt.wantCount, tt.wantTotal, tt.wantAll, tt.wantLabel)
			}
		})
	}
}

func TestOnboardingPermissionSummaryControlsTheExistingGuideAction(t *testing.T) {
	tests := []struct {
		name           string
		accesses       []permissionGuideAccess
		wantSummary    string
		wantGrant      bool
		wantEmptyState bool
	}{
		{name: "all granted", accesses: []permissionGuideAccess{permissionGuideGranted, permissionGuideGranted}, wantGrant: false},
		{name: "partial unknown", accesses: []permissionGuideAccess{permissionGuideGranted, permissionGuideUnknown}, wantSummary: "已授权 1/2", wantGrant: true},
		{name: "empty", wantEmptyState: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &shell{settingsState: settingsState{navigationSymbols: make(map[symbolKey]*ui.Bitmap)}}
			s.onboarding.step = onboardingStepPermissions
			for i, access := range tt.accesses {
				id := []permissionGuideID{permissionGuideInput, permissionGuidePostEvent}[i]
				row := permissionGuideRowFor(id)
				row.access = access
				s.onboarding.permissionSnapshot.rows = append(s.onboarding.permissionSnapshot.rows, row)
			}
			primeOnboardingTestSymbols(s, s.onboarding.permissionSnapshot.rows)
			v := ui.NewTester(s.onboardingView, windowMinWidth, windowMinHeight)
			v.SetDark(false)
			if tt.wantEmptyState {
				if _, found := v.Find("当前无需额外权限"); !found {
					t.Fatal("empty permissions lost their existing empty-state message")
				}
			} else if tt.wantSummary != "" {
				if _, found := v.Find(tt.wantSummary); !found {
					t.Fatalf("missing non-success summary %q", tt.wantSummary)
				}
			}
			_, grantFound := v.Find("授权…")
			if grantFound != tt.wantGrant {
				t.Fatalf("global guide action present=%v, want %v", grantFound, tt.wantGrant)
			}
			_, nextFound := v.Find("开始使用")
			if !nextFound {
				t.Fatal("permission presentation changed the existing explicit completion action")
			}
		})
	}
}

func primeOnboardingTestSymbols(s *shell, rows []permissionGuideRow) {
	if s.navigationSymbols == nil {
		s.navigationSymbols = make(map[symbolKey]*ui.Bitmap)
	}
	for _, dark := range []bool{false, true} {
		theme := appAppearance(dark, s.preferencesState.value.TextSize).theme
		s.navigationSymbols[symbolKey{name: "apple.logo", dark: dark, size: settingsIconSize}] = nil
		s.navigationSymbols[symbolKey{name: "app", dark: dark, size: settingsIconSize}] = nil
		s.navigationSymbols[symbolKey{name: "arrow.left", dark: dark, size: settingsIconSize, tint: theme.Text}] = nil
		s.navigationSymbols[symbolKey{name: "arrow.right", dark: dark, size: settingsIconSize, tint: theme.Background}] = nil
		for _, row := range rows {
			s.navigationSymbols[symbolKey{name: row.icon, dark: dark, size: 20}] = nil
		}
	}
}
