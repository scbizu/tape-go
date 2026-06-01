package tools

import "testing"

func TestExecutionPlanCommandRequestRequiresGoalAndSteps(t *testing.T) {
	req := CommandRequest{
		Goal: "fix config",
		Plan: &ExecutionPlan{
			Goal:  "fix config",
			Steps: []PlanStep{{Name: "check", Script: "pwd"}},
		},
	}

	if err := req.Validate(); err != nil {
		t.Fatalf("expected plan-backed request to be valid: %v", err)
	}
}

func TestExecutionPlanRejectsEmptyStepScript(t *testing.T) {
	plan := ExecutionPlan{
		Goal:  "fix config",
		Steps: []PlanStep{{Name: "check"}},
	}

	if err := plan.Validate(); err == nil {
		t.Fatal("expected empty step script to be rejected")
	}
}
