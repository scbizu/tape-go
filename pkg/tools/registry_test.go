package tools

import (
	"context"
	"strings"
	"testing"
)

func TestRegistryBuildsExecutableArtifactFromPlanBackedRequest(t *testing.T) {
	reg := NewRegistry()

	result, err := reg.Execute(context.Background(), CommandRequest{
		Goal: "run tests",
		Plan: &ExecutionPlan{
			Goal:  "run tests",
			Steps: []PlanStep{{Name: "test", Script: "echo hello"}},
		},
	})
	if err != nil {
		t.Fatalf("registry execute: %v", err)
	}

	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Fatalf("expected stdout hello, got %q", result.Stdout)
	}
}
