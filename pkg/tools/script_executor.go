package tools

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
)

type ExecutionResult struct {
	ExitCode  int
	Stdout    string
	Stderr    string
	Retryable bool
}

type ScriptExecutor struct{}

func NewScriptExecutor() *ScriptExecutor {
	return &ScriptExecutor{}
}

func (e *ScriptExecutor) Execute(ctx context.Context, artifact *ScriptArtifact) (*ExecutionResult, error) {
	path := artifact.Path
	if path == "" {
		tmpFile, err := os.CreateTemp("", "executor-*.sh")
		if err != nil {
			return nil, err
		}
		path = tmpFile.Name()
		if err := tmpFile.Close(); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	defer os.Remove(path)

	if err := os.WriteFile(path, []byte(artifact.Content), 0o700); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "bash", path)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := &ExecutionResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		Retryable: err != nil,
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.ExitCode = exitErr.ExitCode()
	}
	return result, err
}
