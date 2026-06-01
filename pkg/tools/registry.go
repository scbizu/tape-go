package tools

import (
	"context"
	"os"
)

type Registry struct {
	policy   *CommandPolicy
	builder  *ScriptBuilder
	executor *ScriptExecutor
}

func NewRegistry() *Registry {
	return &Registry{
		policy:   NewCommandPolicy(),
		builder:  NewScriptBuilder(os.TempDir()),
		executor: NewScriptExecutor(),
	}
}

func (r *Registry) Execute(ctx context.Context, req CommandRequest) (*ExecutionResult, error) {
	if err := r.policy.Validate(req); err != nil {
		return nil, err
	}
	artifact, err := r.builder.Build(*req.Plan)
	if err != nil {
		return nil, err
	}
	return r.executor.Execute(ctx, artifact)
}
