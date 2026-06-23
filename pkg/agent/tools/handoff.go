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
	SeqS    uint64 `json:"seq_s,omitempty" jsonschema:"First archived entry sequence; zero uses the current tape view."`
	SeqE    uint64 `json:"seq_e,omitempty" jsonschema:"Exclusive archived entry sequence; zero uses the next anchor sequence."`
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
	if tv.Scope.SeqE == 0 {
		return tapeagent.CommandResult{}, fmt.Errorf("tape: handoff empty tape")
	}

	anchorSeq := entry.NextEntryID(tv.Scope.SeqE)
	anchor := entry.HandoffAnchor{
		Summary: args.Summary,
		SeqS:    c.tape.View.SeqS,
		SeqE:    anchorSeq,
	}
	if anchor.SeqS == 0 {
		anchor.SeqS = 1
	}
	if args.SeqS != 0 {
		anchor.SeqS = args.SeqS
	}
	if args.SeqE != 0 {
		anchor.SeqE = args.SeqE
	}
	if anchor.SeqS > anchor.SeqE {
		return tapeagent.CommandResult{}, fmt.Errorf(
			"tape: invalid handoff range [%d,%d)",
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
	c.tape.SetView(view.EntryRange{SeqS: entry.NextEntryID(anchorSeq)})
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
