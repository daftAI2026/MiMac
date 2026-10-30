package main

import (
	"errors"
	"log"

	"mimac.local/mimac/internal/bridge"
	"mimac.local/mimac/internal/platform/inputsource"
)

var (
	errActionInputSource     = errors.New("voice action requires its configured input source")
	errActionInputSourceRead = errors.New("selected input source could not be read")
)

// This runs only at a physical action start, not while drawing settings or on
// key-up. Discovery and readiness UI remain free of input-source polling.
func (s *shell) preflightToolAction(plan bridge.ActionPlan) error {
	var actual string
	err := checkActionInputSource(s.tools, plan, func() (string, error) {
		id, readErr := inputsource.CurrentID()
		actual = id
		return id, readErr
	})
	if err != nil {
		spec := plan.Spec()
		log.Printf("voice action blocked: tool=%q action=%q input_source=%q error=%v", spec.ToolID, spec.ActionID, actual, err)
	}
	s.voiceStartBlocked = ""
	if errors.Is(err, errActionInputSourceRead) {
		s.voiceStartBlocked = "无法读取当前输入法，请重试"
	} else if errors.Is(err, errActionInputSource) {
		for _, tool := range s.tools {
			if tool.id == plan.Spec().ToolID {
				s.voiceStartBlocked = "请切换到" + tool.name
				break
			}
		}
	}
	return err
}

func checkActionInputSource(tools []*voiceTool, plan bridge.ActionPlan, current func() (string, error)) error {
	spec := plan.Spec()
	for _, tool := range tools {
		if tool.id != spec.ToolID {
			continue
		}
		if tool.requiredInputSource == nil {
			return nil
		}
		expected, known := tool.requiredInputSource(spec.ActionID)
		if !known || expected == "" {
			return errActionInputSource
		}
		actual, err := current()
		if err != nil {
			return errors.Join(errActionInputSourceRead, err)
		}
		if actual != expected {
			return errActionInputSource
		}
		return nil
	}
	return errors.New("voice action tool unavailable")
}
