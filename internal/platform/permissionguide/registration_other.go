//go:build !darwin

package permissionguide

import "context"

func PrepareDragRegistration(context.Context, string, RegistrationService) (*DragRegistrationProof, error) {
	return nil, nil
}

func RepairDragRegistrationBeforeDelivery(context.Context, *DragRegistrationProof, RegistrationAccess) (bool, error) {
	return false, nil
}

func DiscardDragRegistrationProof(*DragRegistrationProof) {}

func CanProbeOwnRegistration(RegistrationService) bool { return false }
