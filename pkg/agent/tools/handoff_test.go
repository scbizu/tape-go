package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
)

func TestHandoffCommandDefault(t *testing.T) {
	tape, ctx := newToolTestTape(t)
	if err := tape.Store(ctx, entry.NewEntry(
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent("first"),
	)); err != nil {
		t.Fatal(err)
	}

	anchor := runHandoffCommand(t, tapeagent.NewCommandRegistry(NewHandoffCommand(tape)), ctx, HandoffArgs{})
	if anchor != (entry.HandoffAnchor{SeqS: 1, SeqE: 2}) {
		t.Fatalf("handoff anchor = %#v, want [1,2)", anchor)
	}
	if tape.View != (view.EntryRange{SeqS: 3}) {
		t.Fatalf("tape view = %#v, want SeqS 3", tape.View)
	}
}

func TestHandoffCommandArgs(t *testing.T) {
	tape, ctx := newToolTestTape(t)
	if err := tape.Store(ctx, entry.NewEntry(
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent("first"),
	)); err != nil {
		t.Fatal(err)
	}

	anchor := runHandoffCommand(t, tapeagent.NewCommandRegistry(NewHandoffCommand(tape)), ctx, HandoffArgs{
		Summary: "archived",
		SeqS:    7,
		SeqE:    9,
	})
	if anchor != (entry.HandoffAnchor{Summary: "archived", SeqS: 7, SeqE: 9}) {
		t.Fatalf("handoff anchor = %#v, want custom anchor", anchor)
	}

	entries, err := tape.Range(ctx, view.EntryRange{SeqS: 2, SeqE: 3})
	if err != nil {
		t.Fatal(err)
	}
	var stored entry.HandoffAnchor
	if err := json.Unmarshal([]byte(entries.Raw[0].GetSummary()), &stored); err != nil {
		t.Fatal(err)
	}
	if stored != anchor {
		t.Fatalf("stored anchor = %#v, want %#v", stored, anchor)
	}
}

func TestHandoffCommandEmptyTape(t *testing.T) {
	tape, ctx := newToolTestTape(t)
	_, err := tapeagent.NewCommandRegistry(NewHandoffCommand(tape)).Command(ctx, nil, tapeagent.CommandCall{Name: "handoff"})
	if err == nil || !strings.Contains(err.Error(), "handoff empty tape") {
		t.Fatalf("empty handoff error = %v, want handoff empty tape", err)
	}
}

func TestHandoffCommandNilTape(t *testing.T) {
	_, err := tapeagent.NewCommandRegistry(NewHandoffCommand(nil)).Command(context.Background(), nil, tapeagent.CommandCall{Name: "handoff"})
	if err == nil || !strings.Contains(err.Error(), "nil tape") {
		t.Fatalf("nil tape error = %v, want nil tape", err)
	}
}

func newToolTestTape(t *testing.T) (*tape.Tape, context.Context) {
	t.Helper()
	ownerID := owner.UserID("owner-a")
	store, err := jsonl.NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), ownerID)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return &tape.Tape{TapeStorage: store, OwnerID: ownerID}, ctx
}

func runHandoffCommand(t *testing.T, commands tapeagent.CommandRunner, ctx context.Context, args HandoffArgs) entry.HandoffAnchor {
	t.Helper()
	result, err := commands.Command(ctx, nil, tapeagent.CommandCall{Name: "handoff", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	anchor, ok := result.Data.(entry.HandoffAnchor)
	if !ok {
		t.Fatalf("handoff result = %T, want entry.HandoffAnchor", result.Data)
	}
	return anchor
}
