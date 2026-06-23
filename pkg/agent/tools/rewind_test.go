package tools

import (
	"context"
	"testing"

	tapeagent "github.com/scbizu/tape-go/pkg/agent"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
)

func TestRewindTool(t *testing.T) {
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
	tape := &tape.Tape{TapeStorage: store, OwnerID: ownerID}
	commands := tapeagent.NewCommandRegistry(NewRewindCommand(tape))

	tool, err := NewRewindTool(commands)
	if err != nil {
		t.Fatal(err)
	}
	if tool.Name() != "rewind" {
		t.Fatalf("tool name = %q, want rewind", tool.Name())
	}

	for _, text := range []string{"first", "second"} {
		if err := tape.Store(ctx, entry.NewEntry(
			entry.WithEntryKind(entry.EntryUser),
			entry.WithEntryContent(text),
		)); err != nil {
			t.Fatal(err)
		}
		if err := tape.HandOff(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := tape.Store(ctx, entry.NewEntry(
		entry.WithEntryKind(entry.EntryAssistant),
		entry.WithEntryContent("current"),
	)); err != nil {
		t.Fatal(err)
	}

	viewBefore := tape.View
	got := runRewindCommand(t, commands, ctx, RewindArgs{})
	if got != (view.EntryRange{SeqS: 3, SeqE: 4}) {
		t.Fatalf("default rewind = %#v, want [3,4)", got)
	}
	got = runRewindCommand(t, commands, ctx, RewindArgs{MaxAnchors: 2})
	if got != (view.EntryRange{SeqS: 1, SeqE: 4}) {
		t.Fatalf("two-anchor rewind = %#v, want [1,4)", got)
	}
	got = runRewindCommand(t, commands, ctx, RewindArgs{FromSeq: 2, MaxAnchors: 2})
	if got != (view.EntryRange{SeqS: 1, SeqE: 2}) {
		t.Fatalf("rewind from seq 2 = %#v, want [1,2)", got)
	}
	if tape.View != viewBefore {
		t.Fatalf("rewind changed tape view: got %#v, want %#v", tape.View, viewBefore)
	}
}

func TestRewindWithoutAnchor(t *testing.T) {
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
	commands := tapeagent.NewCommandRegistry(NewRewindCommand(&tape.Tape{TapeStorage: store, OwnerID: ownerID}))

	got := runRewindCommand(t, commands, ctx, RewindArgs{})
	if got != (view.EntryRange{}) {
		t.Fatalf("rewind without anchor = %#v, want empty range", got)
	}
}

func runRewindCommand(t *testing.T, commands tapeagent.CommandRunner, ctx context.Context, args RewindArgs) view.EntryRange {
	t.Helper()
	result, err := commands.Command(ctx, nil, tapeagent.CommandCall{Name: "rewind", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := result.Data.(view.EntryRange)
	if !ok {
		t.Fatalf("rewind result = %T, want view.EntryRange", result.Data)
	}
	return got
}
