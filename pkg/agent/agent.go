// Package agent is the agent runtime
package agent

import (
	"bytes"
	"context"
	"errors"
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
	Command(ctx context.Context, io AgentIO, call CommandCall) (CommandResult, error)
}

type CommandCall struct {
	Name string
	Args any
}

type CommandResult struct {
	Data any
}

type Command interface {
	Name() string
	Run(context.Context, AgentIO, CommandCall) (CommandResult, error)
}

type CommandRunner interface {
	Command(context.Context, AgentIO, CommandCall) (CommandResult, error)
}

type CommandRegistry struct {
	commands map[string]Command
}

type RuntimeOption func(*Runtime)

var _ AgentRuntime = (*Runtime)(nil)

// Runtime is the agent via adk(Google Agent SDK)'s Ralph Loop and harness
type Runtime struct {
	adkagent.Agent
	// commands includes:
	// - built-in bash command
	// - registered command suite
	commands *CommandRegistry
}

// NewRuntime creates a Runtime backed by an ADK LLM agent.
func NewRuntime(cfg llmagent.Config, opts ...RuntimeOption) (*Runtime, error) {
	a, err := llmagent.New(cfg)
	if err != nil {
		return nil, err
	}
	r := &Runtime{
		Agent: a,
		commands: NewCommandRegistry(
			BuiltinBashCommand(),
		),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r, nil
}

func NewCommandRegistry(cmds ...Command) *CommandRegistry {
	r := &CommandRegistry{commands: make(map[string]Command)}
	for _, cmd := range cmds {
		r.Register(cmd)
	}
	return r
}

func (r *CommandRegistry) Register(cmd Command) {
	if cmd == nil {
		return
	}
	if r.commands == nil {
		r.commands = make(map[string]Command)
	}
	r.commands[cmd.Name()] = cmd
}

func (r *CommandRegistry) Command(ctx context.Context, agentIO AgentIO, call CommandCall) (CommandResult, error) {
	if r == nil {
		return CommandResult{}, errors.New("agent: nil command registry")
	}
	cmd, ok := r.commands[call.Name]
	if !ok {
		return CommandResult{}, fmt.Errorf("agent: command %q is not registered", call.Name)
	}
	return cmd.Run(ctx, agentIO, call)
}

func WithCommandRegistry(commands *CommandRegistry) RuntimeOption {
	return func(r *Runtime) {
		if commands != nil {
			r.commands = commands
		}
	}
}

func WithCommand(cmd Command) RuntimeOption {
	return func(r *Runtime) {
		if r.commands == nil {
			r.commands = NewCommandRegistry()
		}
		r.commands.Register(cmd)
	}
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

func (r *Runtime) Command(ctx context.Context, agentIO AgentIO, call CommandCall) (CommandResult, error) {
	return r.commands.Command(ctx, agentIO, call)
}

var _ Command = bashCommand{}

type bashCommand struct{}

type BashArgs struct {
	Command string `json:"command" jsonschema:"Shell command to run."`
}

func BuiltinBashCommand() Command {
	return bashCommand{}
}

func (bashCommand) Name() string { return "bash" }

func (bashCommand) Run(ctx context.Context, agentIO AgentIO, call CommandCall) (CommandResult, error) {
	var command string
	switch args := call.Args.(type) {
	case string:
		command = args
	case BashArgs:
		command = args.Command
	case *BashArgs:
		if args != nil {
			command = args.Command
		}
	default:
		return CommandResult{}, fmt.Errorf("agent: bash args must be string or BashArgs, got %T", call.Args)
	}
	if command == "" {
		return CommandResult{}, errors.New("agent: empty bash command")
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout = agentIO
	cmd.Stderr = agentIO
	return CommandResult{}, cmd.Run()
}
