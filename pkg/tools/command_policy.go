package tools

import "errors"

type CommandRequest struct {
	Raw  string
	Argv []string
	Goal string
	Plan *ExecutionPlan
}

func (req CommandRequest) Validate() error {
	if req.Raw != "" {
		return errors.New("raw shell eval is forbidden")
	}
	if req.Plan == nil {
		return errors.New("execution plan is required")
	}
	if req.Goal == "" {
		return errors.New("goal is required")
	}
	return req.Plan.Validate()
}

type CommandPolicy struct{}

func NewCommandPolicy() *CommandPolicy {
	return &CommandPolicy{}
}

func (p *CommandPolicy) Validate(req CommandRequest) error {
	return req.Validate()
}
