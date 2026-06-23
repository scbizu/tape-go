package tools

import (
	"bytes"
	"context"
	"errors"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
)

type BashResult struct {
	Output string `json:"output,omitempty"`
}

// NewBashTool adapts the built-in bash command for ADK function calls.
func NewBashTool(commands tapeagent.CommandRunner) (tool.Tool, error) {
	if commands == nil {
		return nil, errors.New("agent: nil command runner")
	}
	return functiontool.New(functiontool.Config{
		Name:        "bash",
		Description: "Runs a shell command and returns stdout and stderr.",
	}, func(ctx tool.Context, args tapeagent.BashArgs) (BashResult, error) {
		out := &bashBuffer{}
		if _, err := commands.Command(ctx, out, tapeagent.CommandCall{Name: "bash", Args: args}); err != nil {
			return BashResult{Output: out.String()}, err
		}
		return BashResult{Output: out.String()}, nil
	})
}

type bashBuffer struct {
	bytes.Buffer
}

func (*bashBuffer) Close() error { return nil }

func (b *bashBuffer) Edit(context.Context, []byte) error {
	return errors.New("agent: bash output does not support edit")
}
