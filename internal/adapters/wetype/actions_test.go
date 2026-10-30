package wetype

import "testing"

func TestRequiredInputSourceIDOnlyAppliesToSupportedPTTAction(t *testing.T) {
	if sourceID, ok := RequiredInputSourceID(pttActionID); !ok || sourceID != requiredInputSourceID {
		t.Fatalf("PTT source requirement = %q, %t", sourceID, ok)
	}
	for _, actionID := range []string{toggleActionID, "unknownAction", ""} {
		if sourceID, ok := RequiredInputSourceID(actionID); ok || sourceID != "" {
			t.Errorf("action %q source requirement = %q, %t; want none", actionID, sourceID, ok)
		}
	}
}
