package tools

import "errors"

type PlanStep struct {
	Name   string
	Script string
}

type ExecutionPlan struct {
	Goal  string
	Steps []PlanStep
}

func (p ExecutionPlan) Validate() error {
	if p.Goal == "" {
		return errors.New("goal is required")
	}
	if len(p.Steps) == 0 {
		return errors.New("at least one step is required")
	}
	for _, step := range p.Steps {
		if step.Script == "" {
			return errors.New("step script is required")
		}
	}
	return nil
}
