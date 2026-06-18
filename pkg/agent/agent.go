// Package agent is the agent runtime
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
)

// AgentIO is the agent's IO stream
// - fs
// - network
// - tape
type AgentIO interface {
	io.ReadWriteCloser
	// TODO: Consider context-aware I/O when cancellation of blocking reads becomes necessary.
}

type AgentRuntime interface {
	// Read reads the next context window from `io`
	// Also , Read can read tape or other IOs
	Read(ctx context.Context, io AgentIO) ([]byte, error)
	// Write is the operation same as Read
	Write(ctx context.Context, io AgentIO, data []byte) error
	// Edit IOs
	Edit(ctx context.Context, io AgentIO, data []byte) error
	// Command is the tool use
	// Expose the same execution flow for both agent and user
	Command(ctx context.Context, io AgentIO, cmd string) error
}

var _ AgentRuntime = (*Runtime)(nil)

// Runtime is the agent via adk(Google Agent SDK)'s Ralph Loop and harness
type Runtime struct {
	adkagent.Agent
}

// NewRuntime creates a Runtime backed by an ADK LLM agent.
func NewRuntime(cfg llmagent.Config) (*Runtime, error) {
	a, err := llmagent.New(cfg)
	if err != nil {
		return nil, err
	}
	return &Runtime{Agent: a}, nil
}

func (r *Runtime) Read(_ context.Context, agentIO AgentIO) ([]byte, error) {
	return io.ReadAll(agentIO)
}

func (r *Runtime) Write(_ context.Context, agentIO AgentIO, data []byte) error {
	_, err := io.Copy(agentIO, bytes.NewReader(data))
	return err
}

func (r *Runtime) Edit(ctx context.Context, agentIO AgentIO, data []byte) error {
	editor, ok := agentIO.(interface {
		Edit(context.Context, []byte) error
	})
	if !ok {
		return fmt.Errorf("agent: %T does not support edit", agentIO)
	}
	return editor.Edit(ctx, data)
}

func (r *Runtime) Command(ctx context.Context, agentIO AgentIO, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout = agentIO
	cmd.Stderr = agentIO
	return cmd.Run()
}
