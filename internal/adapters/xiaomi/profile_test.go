package xiaomi

import "testing"

func TestDeviceProfileGeometryMatchesObservedButtonSet(t *testing.T) {
	p, err := DeviceProfile()
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "xiaomi-rc003" || len(p.Buttons) != 13 {
		t.Fatalf("invalid catalog: %+v", p)
	}
	for _, id := range []string{"hid:0007:0052", "hid:0007:0051", "hid:0007:0050", "hid:0007:004f", "hid:0007:0028", "hid:0007:00f1", "hid:0007:004a", "hid:0007:0065", "hid:0007:003e", "hid:0007:0035", "hid:0007:0066", "hid:0007:0080", "hid:0007:0081"} {
		if _, ok := p.Button(id); !ok {
			t.Fatalf("observed button missing: %s", id)
		}
	}
}

func TestArtworkMatchesProfileViewbox(t *testing.T) {
	profile, err := DeviceProfile()
	if err != nil {
		t.Fatal(err)
	}
	artwork, err := Artwork()
	if err != nil {
		t.Fatal(err)
	}
	bounds := artwork.Bounds()
	if float32(bounds.Dx()) != profile.Model.Width || float32(bounds.Dy()) != profile.Model.Height {
		t.Fatal("artwork and hotspots use different coordinates")
	}
	for _, button := range profile.Buttons {
		x := bounds.Min.X + int((button.Bounds.Min.X+button.Bounds.Max.X)/2)
		y := bounds.Min.Y + int((button.Bounds.Min.Y+button.Bounds.Max.Y)/2)
		_, _, _, alpha := artwork.At(x, y).RGBA()
		if alpha < 60000 {
			t.Fatalf("%s hotspot is outside the solid device", button.ID)
		}
	}
}
