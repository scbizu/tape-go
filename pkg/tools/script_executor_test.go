package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptExecutorRunsGeneratedScript(t *testing.T) {
	executor := NewScriptExecutor()

	result, err := executor.Execute(context.Background(), &ScriptArtifact{
		Content: "#!/usr/bin/env bash\nset -euo pipefail\necho hello\n",
	})
	if err != nil {
		t.Fatalf("execute script: %v", err)
	}

	if strings.TrimSpace(result.Stdout) != "hello" {
		t.Fatalf("expected stdout hello, got %q", result.Stdout)
	}
}

func TestScriptExecutorMarksFailuresRetryableWhenScriptExitsNonZero(t *testing.T) {
	executor := NewScriptExecutor()

	result, err := executor.Execute(context.Background(), &ScriptArtifact{
		Content: "#!/usr/bin/env bash\nset -euo pipefail\nexit 2\n",
	})
	if err == nil {
		t.Fatal("expected execution error")
	}

	if !result.Retryable {
		t.Fatal("expected non-policy script failure to be retryable")
	}
}

func TestScriptExecutorUsesArtifactPathWhenProvided(t *testing.T) {
	executor := NewScriptExecutor()
	dir := t.TempDir()
	path := filepath.Join(dir, "provided.sh")

	result, err := executor.Execute(context.Background(), &ScriptArtifact{
		Path:    path,
		Content: "#!/usr/bin/env bash\nset -euo pipefail\necho \"$0\"\n",
	})
	if err != nil {
		t.Fatalf("execute script: %v", err)
	}

	if strings.TrimSpace(result.Stdout) != path {
		t.Fatalf("expected script to run from %q, got %q", path, strings.TrimSpace(result.Stdout))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected one-shot script to be removed, stat err=%v", err)
	}
}
