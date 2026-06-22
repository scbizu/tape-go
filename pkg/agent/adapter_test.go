package agent

import (
	"context"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"

	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
)

func TestTapeAdapterSessionAndContextWindow(t *testing.T) {
	ownerID := owner.UserID("owner-a")
	userTimestamp := time.Now().Add(time.Hour)
	agentTimestamp := userTimestamp.Add(time.Hour)
	store, err := jsonl.NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), ownerID)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	adapter, err := NewTapeAdapter(&tape.Tape{TapeStorage: store, OwnerID: ownerID}, "app-a")
	if err != nil {
		t.Fatal(err)
	}
	created, err := adapter.Create(ctx, &session.CreateRequest{
		AppName: "app-a", UserID: ownerID, SessionID: "session-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := session.NewEvent("invocation-a")
	event.Author = owner.SystemUser
	event.Timestamp = userTimestamp
	event.LLMResponse = model.LLMResponse{Content: &genai.Content{
		Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}},
	}}
	if err := adapter.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Tape.HandOff(ctx); err != nil {
		t.Fatal(err)
	}
	event = session.NewEvent("invocation-b")
	event.Author = owner.SystemAgent
	event.Timestamp = agentTimestamp
	event.LLMResponse = model.LLMResponse{Content: &genai.Content{
		Role: genai.RoleModel, Parts: []*genai.Part{{Text: "current"}},
	}}
	if err := adapter.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}

	got, err := adapter.Get(ctx, &session.GetRequest{
		AppName: "app-a", UserID: ownerID, SessionID: "session-a", After: agentTimestamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Session.Events().Len() != 1 || got.Session.Events().At(0).Content.Parts[0].Text != "current" {
		t.Fatalf("session event mismatch: %#v", got.Session.Events())
	}
	if !got.Session.Events().At(0).Timestamp.Equal(agentTimestamp) || !got.Session.LastUpdateTime().Equal(agentTimestamp) {
		t.Fatalf("session timestamp mismatch: event=%v updated=%v", got.Session.Events().At(0).Timestamp, got.Session.LastUpdateTime())
	}

	req := &model.LLMRequest{}
	if _, err := adapter.ContextWindow(nil, req); err != nil {
		t.Fatal(err)
	}
	if len(req.Contents) != 1 || req.Contents[0].Parts[0].Text != "current" {
		t.Fatalf("context window mismatch: %#v", req.Contents)
	}
}

func TestTapeAdapterRewindTool(t *testing.T) {
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
	adapter, err := NewTapeAdapter(tape, "app-a")
	if err != nil {
		t.Fatal(err)
	}

	tool, err := adapter.RewindTool()
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
	got, err := adapter.rewind(ctx, rewindArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if got != (view.EntryRange{SeqS: 3, SeqE: 4}) {
		t.Fatalf("default rewind = %#v, want [3,4)", got)
	}
	got, err = adapter.rewind(ctx, rewindArgs{MaxAnchors: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got != (view.EntryRange{SeqS: 1, SeqE: 4}) {
		t.Fatalf("two-anchor rewind = %#v, want [1,4)", got)
	}
	got, err = adapter.rewind(ctx, rewindArgs{FromSeq: 2, MaxAnchors: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got != (view.EntryRange{SeqS: 1, SeqE: 2}) {
		t.Fatalf("rewind from seq 2 = %#v, want [1,2)", got)
	}
	if tape.View != viewBefore {
		t.Fatalf("rewind changed tape view: got %#v, want %#v", tape.View, viewBefore)
	}

	req := &model.LLMRequest{}
	if _, err := adapter.ContextWindow(nil, req); err != nil {
		t.Fatal(err)
	}
	if len(req.Contents) != 1 || req.Contents[0].Parts[0].Text != "current" {
		t.Fatalf("context window mismatch after rewind: %#v", req.Contents)
	}
}

func TestTapeAdapterRewindWithoutAnchor(t *testing.T) {
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
	adapter, err := NewTapeAdapter(&tape.Tape{TapeStorage: store, OwnerID: ownerID}, "app-a")
	if err != nil {
		t.Fatal(err)
	}

	got, err := adapter.rewind(ctx, rewindArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if got != (view.EntryRange{}) {
		t.Fatalf("rewind without anchor = %#v, want empty range", got)
	}
}

func TestEventFromEntryTimestamp(t *testing.T) {
	want := time.Unix(123, 0)
	event, err := eventFromEntry(entry.NewEntry(entry.WithEntryTimestamp(want)))
	if err != nil {
		t.Fatal(err)
	}
	if !event.Timestamp.Equal(want) {
		t.Fatalf("timestamp = %v, want %v", event.Timestamp, want)
	}
}
