package tools

import (
	"strings"
	"testing"
)

func TestScriptBuilderBuildsOneShotScriptWithSafetyHeader(t *testing.T) {
	builder := NewScriptBuilder("/tmp")

	artifact, err := builder.Build(ExecutionPlan{
		Goal:  "run tests",
		Steps: []PlanStep{{Name: "test", Script: "go test ./..."}},
	})
	if err != nil {
		t.Fatalf("build script: %v", err)
	}

	if !strings.Contains(artifact.Content, "set -euo pipefail") {
		t.Fatal("expected safety header in script")
	}
}

func TestScriptBuilderIncludesPlanStepsInOrder(t *testing.T) {
	builder := NewScriptBuilder("/tmp")

	artifact, err := builder.Build(ExecutionPlan{
		Goal: "fix",
		Steps: []PlanStep{
			{Name: "check", Script: "pwd"},
			{Name: "apply", Script: "go test ./..."},
		},
	})
	if err != nil {
		t.Fatalf("build script: %v", err)
	}

	if strings.Index(artifact.Content, "pwd") > strings.Index(artifact.Content, "go test ./...") {
		t.Fatal("expected script steps to preserve plan order")
	}
}

func TestScriptBuilderBuildsUniqueArtifactPaths(t *testing.T) {
	builder := NewScriptBuilder("/tmp")

	first, err := builder.Build(ExecutionPlan{
		Goal:  "first",
		Steps: []PlanStep{{Name: "first", Script: "echo first"}},
	})
	if err != nil {
		t.Fatalf("build first script: %v", err)
	}

	second, err := builder.Build(ExecutionPlan{
		Goal:  "second",
		Steps: []PlanStep{{Name: "second", Script: "echo second"}},
	})
	if err != nil {
		t.Fatalf("build second script: %v", err)
	}

	if first.Path == second.Path {
		t.Fatalf("expected unique artifact paths, got %q", first.Path)
	}
}
