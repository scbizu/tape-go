// Package tools provides runtime commands and provider-specific tool adapters.
package tools

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type RewindArgs struct {
	FromSeq    uint64 `json:"from_seq,omitempty" jsonschema:"Entry sequence to rewind from; zero means the latest entry."`
	MaxAnchors uint8  `json:"max_anchors,omitempty" jsonschema:"Maximum anchors to rewind; zero defaults to one."`
}

type rewindCommand struct {
	tape *tape.Tape
}

func NewRewindCommand(t *tape.Tape) tapeagent.Command {
	return rewindCommand{tape: t}
}

func (rewindCommand) Name() string { return "rewind" }

func (c rewindCommand) Run(ctx context.Context, _ tapeagent.AgentIO, call tapeagent.CommandCall) (tapeagent.CommandResult, error) {
	if c.tape == nil {
		return tapeagent.CommandResult{}, errors.New("agent: nil tape")
	}
	var args RewindArgs
	switch v := call.Args.(type) {
	case RewindArgs:
		args = v
	case *RewindArgs:
		if v != nil {
			args = *v
		}
	case nil:
	default:
		return tapeagent.CommandResult{}, fmt.Errorf("agent: rewind args must be RewindArgs, got %T", call.Args)
	}
	tapeCtx := ctx
	if tapeCtx == nil {
		tapeCtx = context.Background()
	}
	tapeCtx = owner.WithOwnerId(tapeCtx, c.tape.OwnerID)
	r, err := c.tape.Rewind(
		tapeCtx,
		storage.WithRewindFromSeq(args.FromSeq),
		storage.WithRewindMaxAnchors(args.MaxAnchors),
	)
	if errors.Is(err, storage.ErrNoAnchor) {
		return tapeagent.CommandResult{Data: view.EntryRange{}}, nil
	}
	if err != nil {
		return tapeagent.CommandResult{}, err
	}
	return tapeagent.CommandResult{Data: r}, nil
}

func NewRewindTool(commands tapeagent.CommandRunner) (tool.Tool, error) {
	if commands == nil {
		return nil, errors.New("agent: nil command runner")
	}
	return functiontool.New(functiontool.Config{
		Name:        "rewind",
		Description: "Returns an earlier context window range referenced by tape anchors.",
	}, func(ctx tool.Context, args RewindArgs) (view.EntryRange, error) {
		result, err := commands.Command(ctx, nil, tapeagent.CommandCall{Name: "rewind", Args: args})
		if err != nil {
			return view.EntryRange{}, err
		}
		r, ok := result.Data.(view.EntryRange)
		if !ok {
			return view.EntryRange{}, fmt.Errorf("agent: rewind result must be view.EntryRange, got %T", result.Data)
		}
		return r, nil
	})
}
