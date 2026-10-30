package wetype

const requiredInputSourceID = "com.tencent.inputmethod.wetype.pinyin"

// RequiredInputSourceID reports runtime input-source requirements for verified
// WeType action contracts. Global scope describes the shortcut's configured
// scope; the host must still select and verify the required source before
// starting the action.
func RequiredInputSourceID(actionID string) (string, bool) {
	if actionID == pttActionID {
		return requiredInputSourceID, true
	}
	return "", false
}
