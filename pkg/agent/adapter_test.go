package agent

import (
	"bytes"
	"context"
	"encoding/json"
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

type bufferIO struct {
	bytes.Buffer
}

func (*bufferIO) Close() error { return nil }

func TestBuiltinBashCommand(t *testing.T) {
	runtime := &Runtime{commands: NewCommandRegistry(BuiltinBashCommand())}
	out := &bufferIO{}
	if _, err := runtime.Command(context.Background(), out, CommandCall{
		Name: "bash",
		Args: BashArgs{Command: "printf hello"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "hello" {
		t.Fatalf("bash output = %q, want hello", got)
	}
}

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
	payload, err := json.Marshal(entry.HandoffAnchor{SeqS: 1, SeqE: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Tape.Store(ctx, entry.NewAnchor(2, ownerID, entry.AnchorKindHandoff, payload)); err != nil {
		t.Fatal(err)
	}
	adapter.Tape.SetView(view.EntryRange{SeqS: 3})
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
