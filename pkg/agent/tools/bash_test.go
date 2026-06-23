package tools

import (
	"context"
	"testing"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
)

func TestBashTool(t *testing.T) {
	tool, err := NewBashTool(tapeagent.NewCommandRegistry(tapeagent.BuiltinBashCommand()))
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name() != "bash" {
		t.Fatalf("tool name = %q, want bash", tool.Name())
	}

	result, err := runBashCommand(t, tapeagent.NewCommandRegistry(tapeagent.BuiltinBashCommand()), tapeagent.BashArgs{Command: "printf hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "hello" {
		t.Fatalf("bash output = %q, want hello", result.Output)
	}
}

func runBashCommand(t *testing.T, commands tapeagent.CommandRunner, args tapeagent.BashArgs) (BashResult, error) {
	t.Helper()
	out := &bashBuffer{}
	_, err := commands.Command(context.Background(), out, tapeagent.CommandCall{Name: "bash", Args: args})
	return BashResult{Output: out.String()}, err
}
