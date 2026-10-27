// Package devices contains platform-neutral, immutable-by-convention device
// profiles for describing physical buttons and simple model geometry.
package devices

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

var ErrInvalidProfile = errors.New("devices: invalid profile")

// Point is a coordinate in a Model's local viewbox units.
type Point struct {
	X float32
	Y float32
}

// Rect is an axis-aligned rectangle in a Model's local viewbox units.
type Rect struct {
	Min Point
	Max Point
}

// Button describes one physical button. ID is stable within a device profile;
// Bounds and CornerRadius use the enclosing Model's local viewbox units.
type Button struct {
	ID           string
	Label        string
	Symbol       string
	DefaultLabel string // Profile/HID key label, not a confirmed macOS action; display-only, never synthesized.
	Bounds       Rect
	CornerRadius float32
}

// Surface is a simple decorative rectangle in a Model's local viewbox units.
// It carries geometry only; rendering style belongs to the host UI.
type Surface struct {
	Bounds       Rect
	CornerRadius float32
}

// Model contains only display geometry and an optional initial selection.
// It has no rendering-framework or mutable interaction state.
type Model struct {
	Width                   float32
	Height                  float32
	CornerRadius            float32
	DefaultSelectedButtonID string
	Surfaces                []Surface
}

// Profile binds stable physical button IDs to display metadata and geometry.
// Call NewProfile at ownership boundaries to validate and copy Button storage.
type Profile struct {
	ID      string
	Name    string
	Buttons []Button
	Model   Model
}

// NewProfile validates profile and returns a copy that does not share its
// button or surface slices with the caller.
func NewProfile(profile Profile) (Profile, error) {
	profile = profile.Clone()
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Validate checks IDs, labels, viewbox geometry, button bounds, and the model's
// optional default button reference.
func (profile Profile) Validate() error {
	if !validText(profile.ID) || !validText(profile.Name) {
		return fmt.Errorf("%w: profile ID and name are required", ErrInvalidProfile)
	}
	if len(profile.Buttons) == 0 {
		return fmt.Errorf("%w: profile has no buttons", ErrInvalidProfile)
	}
	if !positiveFinite(profile.Model.Width) || !positiveFinite(profile.Model.Height) {
		return fmt.Errorf("%w: model width and height must be finite and positive", ErrInvalidProfile)
	}
	if !validCornerRadius(profile.Model.CornerRadius, min32(profile.Model.Width, profile.Model.Height)) {
		return fmt.Errorf("%w: model corner radius is outside its geometry", ErrInvalidProfile)
	}
	for i, surface := range profile.Model.Surfaces {
		if !validRect(surface.Bounds, profile.Model.Width, profile.Model.Height) {
			return fmt.Errorf("%w: model surface %d has invalid bounds", ErrInvalidProfile, i)
		}
		if !validCornerRadius(surface.CornerRadius, min32(surface.Bounds.Max.X-surface.Bounds.Min.X, surface.Bounds.Max.Y-surface.Bounds.Min.Y)) {
			return fmt.Errorf("%w: model surface %d has invalid corner radius", ErrInvalidProfile, i)
		}
	}

	buttonIDs := make(map[string]struct{}, len(profile.Buttons))
	for i, button := range profile.Buttons {
		if !validText(button.ID) || !validText(button.Label) || !validOptionalText(button.Symbol) || !validOptionalText(button.DefaultLabel) {
			return fmt.Errorf("%w: button %d requires a valid ID and label", ErrInvalidProfile, i)
		}
		if _, exists := buttonIDs[button.ID]; exists {
			return fmt.Errorf("%w: duplicate button ID %q", ErrInvalidProfile, button.ID)
		}
		buttonIDs[button.ID] = struct{}{}
		if !validRect(button.Bounds, profile.Model.Width, profile.Model.Height) {
			return fmt.Errorf("%w: button %q has invalid bounds", ErrInvalidProfile, button.ID)
		}
		if !validCornerRadius(button.CornerRadius, min32(button.Bounds.Max.X-button.Bounds.Min.X, button.Bounds.Max.Y-button.Bounds.Min.Y)) {
			return fmt.Errorf("%w: button %q has invalid corner radius", ErrInvalidProfile, button.ID)
		}
	}
	if profile.Model.DefaultSelectedButtonID != "" {
		if !validText(profile.Model.DefaultSelectedButtonID) {
			return fmt.Errorf("%w: invalid default selected button ID", ErrInvalidProfile)
		}
		if _, exists := buttonIDs[profile.Model.DefaultSelectedButtonID]; !exists {
			return fmt.Errorf("%w: default selected button %q does not exist", ErrInvalidProfile, profile.Model.DefaultSelectedButtonID)
		}
	}
	return nil
}

// Clone returns a deep copy of Profile's mutable slices.
func (profile Profile) Clone() Profile {
	profile.Buttons = append([]Button(nil), profile.Buttons...)
	profile.Model.Surfaces = append([]Surface(nil), profile.Model.Surfaces...)
	return profile
}

// Button returns a value copy of the button with id.
func (profile Profile) Button(id string) (Button, bool) {
	for _, button := range profile.Buttons {
		if button.ID == id {
			return button, true
		}
	}
	return Button{}, false
}

func validRect(rect Rect, width, height float32) bool {
	return finite(rect.Min.X) && finite(rect.Min.Y) && finite(rect.Max.X) && finite(rect.Max.Y) &&
		rect.Min.X >= 0 && rect.Min.Y >= 0 && rect.Max.X <= width && rect.Max.Y <= height &&
		rect.Min.X < rect.Max.X && rect.Min.Y < rect.Max.Y
}

func validCornerRadius(radius, shortestSide float32) bool {
	// Centered float32 bounds can lose a final bit when subtracting an
	// absolute anchor. Accept that rounding, not visibly oversized radii.
	return finite(radius) && radius >= 0 && radius <= shortestSide/2+0.0001
}

func validText(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsRune(value, '\x00') && utf8.ValidString(value)
}

func validOptionalText(value string) bool {
	return value == "" || validText(value)
}

func positiveFinite(value float32) bool { return finite(value) && value > 0 }

func finite(value float32) bool {
	return !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
}

func min32(left, right float32) float32 {
	if left < right {
		return left
	}
	return right
}
