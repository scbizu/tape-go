// Package agent is the agent runtime
package agent

import (
	"context"
	"io"
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
	// Read reads in background for the next context window
	// Also , Read can read tape or other IOs
	Read(ctx context.Context, io AgentIO) ([]byte, error)
	// Write is the operation same as Read
	Write(ctx context.Context, data []byte) (AgentIO, error)
	// Edit IOs
	Edit(ctx context.Context, io AgentIO, data []byte) (AgentIO, error)
	// Command is the tool use
	// Expose the same execution flow for both agent and user
	Command(ctx context.Context, io AgentIO, cmd string) error
}
