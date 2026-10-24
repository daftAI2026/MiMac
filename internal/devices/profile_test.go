package devices

import (
	"errors"
	"math"
	"testing"
)

func validProfile() Profile {
	return Profile{
		ID:   "example.remote",
		Name: "Example Remote",
		Buttons: []Button{{
			ID:           "hid:0007:0028",
			Label:        "确认",
			Symbol:       "checkmark",
			Bounds:       Rect{Min: Point{X: 40, Y: 90}, Max: Point{X: 60, Y: 110}},
			CornerRadius: 2,
		}},
		Model: Model{
			Width:                   100,
			Height:                  200,
			CornerRadius:            16,
			DefaultSelectedButtonID: "hid:0007:0028",
			Surfaces: []Surface{{
				Bounds:       Rect{Min: Point{X: 20, Y: 20}, Max: Point{X: 80, Y: 30}},
				CornerRadius: 2,
			}},
		},
	}
}

func TestNewProfileCopiesButtonsAndCloneIsIndependent(t *testing.T) {
	source := validProfile()
	profile, err := NewProfile(source)
	if err != nil {
		t.Fatal(err)
	}
	source.Buttons[0].Label = "changed source"
	source.Model.Surfaces[0].CornerRadius = 9
	if got := profile.Buttons[0].Label; got != "确认" {
		t.Fatalf("constructor shared input buttons: %q", got)
	}
	if got := profile.Model.Surfaces[0].CornerRadius; got != 2 {
		t.Fatalf("constructor shared input surfaces: %v", got)
	}

	clone := profile.Clone()
	clone.Buttons[0].Label = "changed clone"
	clone.Model.Surfaces[0].CornerRadius = 8
	if got := profile.Buttons[0].Label; got != "确认" {
		t.Fatalf("clone shared button storage: %q", got)
	}
	if got := profile.Model.Surfaces[0].CornerRadius; got != 2 {
		t.Fatalf("clone shared surface storage: %v", got)
	}

	button, ok := profile.Button("hid:0007:0028")
	if !ok || button.Label != "确认" {
		t.Fatalf("Button lookup = %#v, %v", button, ok)
	}
	button.Label = "changed accessor"
	if got := profile.Buttons[0].Label; got != "确认" {
		t.Fatalf("Button lookup exposed profile storage: %q", got)
	}
	if _, ok := profile.Button("missing"); ok {
		t.Fatal("lookup found an unknown button")
	}
}

func TestProfileRejectsDuplicateButtonIDsAndUnknownDefaultReference(t *testing.T) {
	duplicate := validProfile()
	duplicate.Buttons = append(duplicate.Buttons, duplicate.Buttons[0])
	if _, err := NewProfile(duplicate); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("duplicate button error = %v", err)
	}

	unknownDefault := validProfile()
	unknownDefault.Model.DefaultSelectedButtonID = "hid:0007:ffff"
	if _, err := NewProfile(unknownDefault); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("unknown default button error = %v", err)
	}
}

func TestProfileRejectsInvalidGeometry(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Profile)
	}{
		{
			name: "nonpositive model size",
			change: func(profile *Profile) {
				profile.Model.Width = 0
			},
		},
		{
			name: "nonfinite model size",
			change: func(profile *Profile) {
				profile.Model.Height = float32(math.NaN())
			},
		},
		{
			name: "button outside model",
			change: func(profile *Profile) {
				profile.Buttons[0].Bounds.Max.X = 101
			},
		},
		{
			name: "empty button bounds",
			change: func(profile *Profile) {
				profile.Buttons[0].Bounds.Max.X = profile.Buttons[0].Bounds.Min.X
			},
		},
		{
			name: "excessive button radius",
			change: func(profile *Profile) {
				profile.Buttons[0].CornerRadius = 11
			},
		},
		{
			name: "negative model radius",
			change: func(profile *Profile) {
				profile.Model.CornerRadius = -1
			},
		},
		{
			name: "surface outside model",
			change: func(profile *Profile) {
				profile.Model.Surfaces[0].Bounds.Max.X = 101
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profile := validProfile()
			test.change(&profile)
			if err := profile.Validate(); !errors.Is(err, ErrInvalidProfile) {
				t.Fatalf("Validate error = %v", err)
			}
		})
	}
}
