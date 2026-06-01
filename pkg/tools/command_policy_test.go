package tools

import "testing"

func TestCommandPolicy_RejectRawShellEval(t *testing.T) {
	p := NewCommandPolicy()
	err := p.Validate(CommandRequest{Raw: "rm -rf /"})
	if err == nil {
		t.Fatal("expected rejection for raw shell eval")
	}
}

func TestCommandPolicy_RejectsCommandWithoutExecutionPlan(t *testing.T) {
	p := NewCommandPolicy()

	err := p.Validate(CommandRequest{
		Argv: []string{"go", "test", "./..."},
	})
	if err == nil {
		t.Fatal("expected direct argv execution to be rejected")
	}
}

func TestCommandPolicy_AllowsPlanBackedCommandRequest(t *testing.T) {
	p := NewCommandPolicy()

	err := p.Validate(CommandRequest{
		Goal: "run tests",
		Plan: &ExecutionPlan{
			Goal:  "run tests",
			Steps: []PlanStep{{Name: "test", Script: "go test ./..."}},
		},
	})
	if err != nil {
		t.Fatalf("expected plan-backed command request to pass: %v", err)
	}
}
