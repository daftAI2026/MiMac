package main

import (
	"context"
	"fmt"

	"mimac.local/mimac/internal/platform/audioroute"
)

// compatApplicationInspector separates a running application's identity from
// its lazy Core Audio registration. An absent audio client is idle, never ready
// to receive PCM. The receiver gate still waits for the actual PID and UID.
type compatApplicationInspector struct {
	compatAudioInspector
	bundleID string
	resolve  func(context.Context) (int32, error)
}

func (i *compatApplicationInspector) InputsForBundle(bundleID string) ([]audioroute.ProcessInput, error) {
	if bundleID != i.bundleID || i.resolve == nil {
		return nil, errCompatRouteTarget
	}
	pid, err := i.resolve(context.Background())
	if err != nil {
		return nil, err
	}
	clients, err := i.compatAudioInspector.InputsForBundle(bundleID)
	if err != nil {
		return nil, err
	}
	if len(clients) > 1 {
		return nil, errCompatRouteTargetMissing
	}
	for _, client := range clients {
		if !client.Found || client.PID != pid || pid <= 0 {
			return nil, errCompatRouteTargetMissing
		}
	}
	if pid <= 0 {
		return nil, nil
	}
	if len(clients) == 1 {
		return clients, nil
	}
	return []audioroute.ProcessInput{{Found: true, PID: pid}}, nil
}

func (i *compatApplicationInspector) ProcessInput(pid int32) (audioroute.ProcessInput, error) {
	processes, err := i.InputsForBundle(i.bundleID)
	if err != nil {
		return audioroute.ProcessInput{}, err
	}
	if len(processes) != 1 || processes[0].PID != pid {
		return audioroute.ProcessInput{PID: pid}, nil
	}
	input, err := i.compatAudioInspector.ProcessInput(pid)
	if err != nil {
		return audioroute.ProcessInput{}, err
	}
	if !input.Found {
		return audioroute.ProcessInput{Found: true, PID: pid}, nil
	}
	if input.PID != pid {
		return audioroute.ProcessInput{}, errCompatRouteTargetMissing
	}
	return input, nil
}

func (i *compatApplicationInspector) WaitForInput(ctx context.Context, pid int32, uid string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	processes, err := i.InputsForBundle(i.bundleID)
	if err != nil {
		return err
	}
	if len(processes) != 1 || processes[0].PID != pid {
		return fmt.Errorf("mimac-compat receiver: %w", errCompatRouteTargetMissing)
	}
	return i.compatAudioInspector.WaitForInput(ctx, pid, uid)
}
