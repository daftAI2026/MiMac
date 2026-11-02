package xiaomi

import "mimac.local/mimac/internal/devices"

// VoiceButtonID identifies physical input, not an output shortcut.
const VoiceButtonID = "hid:0007:003e"

// DeviceProfile pairs observed HID identities with hotspots in Artwork's
// pixel viewbox. These are visual hit areas, not physical dimensions.
func DeviceProfile() (devices.Profile, error) {
	return devices.NewProfile(devices.Profile{
		ID: "xiaomi-rc003", Name: "小米蓝牙遥控器 2 Pro",
		Model: devices.Model{Width: 146, Height: 572, CornerRadius: 8, DefaultSelectedButtonID: VoiceButtonID},
		Buttons: []devices.Button{
			rc003Button(VoiceButtonID, "语音", "voice", "F5", 110, 39, 36, 36, 18),
			rc003Button("hid:0007:0066", "电源", "power", "Power", 35, 39, 36, 36, 18),
			rc003Button("hid:0007:0052", "上", "arrow-up", "↑", 73, 83, 24, 24, 12),
			rc003Button("hid:0007:0050", "左", "arrow-left", "←", 25, 130, 24, 24, 12),
			rc003Button("hid:0007:0028", "确认", "confirm", "↩", 73, 130, 60, 60, 30),
			rc003Button("hid:0007:004f", "右", "arrow-right", "→", 120, 130, 24, 24, 12),
			rc003Button("hid:0007:0051", "下", "arrow-down", "↓", 73, 176, 24, 24, 12),
			rc003Button("hid:0007:00f1", "返回", "back", "未定义", 43, 219, 48, 48, 24),
			rc003Button("hid:0007:0080", "音量 +", "volume-up", "音量 +", 103, 219, 48, 48, 24),
			rc003Button("hid:0007:0081", "音量 −", "volume-down", "音量 −", 103, 274, 48, 48, 24),
			rc003Button("hid:0007:004a", "主页", "home", "Home", 43, 274, 48, 48, 24),
			rc003Button("hid:0007:0065", "菜单", "menu", "菜单键", 43, 329, 48, 48, 24),
			rc003Button("hid:0007:0035", "TV", "tv", "` / ~", 103, 329, 48, 48, 24),
		},
	})
}

func rc003Button(id, label, symbol, defaultLabel string, x, y, width, height, radius float32) devices.Button {
	return devices.Button{ID: id, Label: label, Symbol: symbol, DefaultLabel: defaultLabel, CornerRadius: radius, Bounds: devices.Rect{
		Min: devices.Point{X: x - width/2, Y: y - height/2}, Max: devices.Point{X: x + width/2, Y: y + height/2},
	}}
}
