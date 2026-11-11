package permissionguide

import (
	"strconv"
	"strings"
)

func accessibilityPermissionTitle(productVersion string) string {
	major, err := strconv.Atoi(strings.SplitN(productVersion, ".", 2)[0])
	if err == nil && major >= 27 {
		return "设备控制和数据访问"
	}
	return "辅助功能"
}
