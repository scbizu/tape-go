package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// HandoffArgs configures the handoff command's anchor payload and range.
type HandoffArgs struct {
	Summary string `json:"summary,omitempty" jsonschema:"Summary for the archived context window."`
	SeqS    string `json:"seq_s,omitempty" jsonschema:"First archived entry sequence as a decimal string; empty or zero uses the current tape view."`
	SeqE    string `json:"seq_e,omitempty" jsonschema:"Exclusive archived entry sequence as a decimal string; empty or zero uses the next anchor sequence."`
}

type handoffCommand struct {
	tape *tape.Tape
}

// NewHandoffCommand returns a command that writes handoff anchors to t.
func NewHandoffCommand(t *tape.Tape) tapeagent.Command {
	return handoffCommand{tape: t}
}

func (handoffCommand) Name() string { return "handoff" }

func (c handoffCommand) Run(ctx context.Context, _ tapeagent.AgentIO, call tapeagent.CommandCall) (tapeagent.CommandResult, error) {
	if c.tape == nil {
		return tapeagent.CommandResult{}, errors.New("agent: nil tape")
	}
	args, err := handoffArgs(call.Args)
	if err != nil {
		return tapeagent.CommandResult{}, err
	}
	tapeCtx := ctx
	if tapeCtx == nil {
		tapeCtx = context.Background()
	}
	tapeCtx = owner.WithOwnerId(tapeCtx, c.tape.OwnerID)

	tv, err := c.tape.Get(tapeCtx)
	if err != nil {
		return tapeagent.CommandResult{}, fmt.Errorf("tape: %w", err)
	}
	if tv.Scope.SeqE.IsZero() {
		return tapeagent.CommandResult{}, fmt.Errorf("tape: handoff empty tape")
	}

	anchorSeq := tv.Scope.SeqE.Next()
	anchor := entry.HandoffAnchor{
		Summary: args.Summary,
		SeqS:    c.tape.View.SeqS,
		SeqE:    anchorSeq,
	}
	if anchor.SeqS.IsZero() {
		anchor.SeqS = entry.SeqFromUint64(1)
	}
	if args.SeqS != "" {
		seq, err := entry.ParseSeq(args.SeqS)
		if err != nil {
			return tapeagent.CommandResult{}, fmt.Errorf("agent: handoff seq_s: %w", err)
		}
		if !seq.IsZero() {
			anchor.SeqS = seq
		}
	}
	if args.SeqE != "" {
		seq, err := entry.ParseSeq(args.SeqE)
		if err != nil {
			return tapeagent.CommandResult{}, fmt.Errorf("agent: handoff seq_e: %w", err)
		}
		if !seq.IsZero() {
			anchor.SeqE = seq
		}
	}
	if anchor.SeqS.Cmp(anchor.SeqE) > 0 {
		return tapeagent.CommandResult{}, fmt.Errorf(
			"tape: invalid handoff range [%s,%s)",
			anchor.SeqS,
			anchor.SeqE,
		)
	}

	payload, err := json.Marshal(anchor)
	if err != nil {
		return tapeagent.CommandResult{}, fmt.Errorf("tape: marshal handoff anchor: %w", err)
	}
	if err := c.tape.Store(
		tapeCtx,
		entry.NewAnchor(anchorSeq, tv.Owner, entry.AnchorKindHandoff, payload),
	); err != nil {
		return tapeagent.CommandResult{}, fmt.Errorf("tape: %w", err)
	}
	c.tape.SetView(view.EntryRange{SeqS: anchorSeq.Next()})
	return tapeagent.CommandResult{Data: anchor}, nil
}

// NewHandoffTool adapts the handoff command for ADK function calls.
func NewHandoffTool(commands tapeagent.CommandRunner) (tool.Tool, error) {
	if commands == nil {
		return nil, errors.New("agent: nil command runner")
	}
	return functiontool.New(functiontool.Config{
		Name:        "handoff",
		Description: "Writes a handoff anchor for the current tape context window.",
	}, func(ctx tool.Context, args HandoffArgs) (entry.HandoffAnchor, error) {
		result, err := commands.Command(ctx, nil, tapeagent.CommandCall{Name: "handoff", Args: args})
		if err != nil {
			return entry.HandoffAnchor{}, err
		}
		anchor, ok := result.Data.(entry.HandoffAnchor)
		if !ok {
			return entry.HandoffAnchor{}, fmt.Errorf("agent: handoff result must be entry.HandoffAnchor, got %T", result.Data)
		}
		return anchor, nil
	})
}

func handoffArgs(raw any) (HandoffArgs, error) {
	switch v := raw.(type) {
	case HandoffArgs:
		return v, nil
	case *HandoffArgs:
		if v != nil {
			return *v, nil
		}
		return HandoffArgs{}, nil
	case nil:
		return HandoffArgs{}, nil
	default:
		return HandoffArgs{}, fmt.Errorf("agent: handoff args must be HandoffArgs, got %T", raw)
	}
}
