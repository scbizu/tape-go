package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"

	jevext "github.com/scbizu/tape-go/pkg/ext/jev"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
)

type searchDecider struct{}

func (searchDecider) ShouldAnchor(context.Context, jevext.ViewProjection) (float64, error) {
	return 0, nil
}
func (searchDecider) ValidateSummary(context.Context, jevext.ViewProjection, jevext.MemoryState) (float64, error) {
	return 1, nil
}

type searchSummarizer struct{}

func (searchSummarizer) Summarize(context.Context, jevext.ViewProjection) (jevext.MemoryState, error) {
	return jevext.MemoryState{Decisions: []string{"saved"}}, nil
}

type searchClassifier struct{}

func (searchClassifier) Classify(context.Context, string, []jevext.MemoryState) iter.Seq2[jevext.Classification, error] {
	return func(yield func(jevext.Classification, error) bool) {
		yield(jevext.Classification{Index: 0, Score: 1}, nil)
	}
}

func TestSearchMemoryUsesConfiguredTapeExtension(t *testing.T) {
	ownerID := owner.UserID("owner-a")
	ctx := owner.WithOwnerId(context.Background(), ownerID)
	base, err := jsonl.NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	base.Fs = afero.NewMemMapFs()
	tape := &tape.Tape{OwnerID: ownerID}
	decorated, err := jevext.NewStorage(base, &tape.View, jevext.Config{
		Decider: searchDecider{}, Summarizer: searchSummarizer{}, Classifier: searchClassifier{}, Threshold: .7,
	})
	if err != nil {
		t.Fatal(err)
	}
	tape.TapeStorage = decorated
	if err := tape.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := tape.Store(ctx, entry.NewEntry(entry.WithEntryKind(entry.EntryUser), entry.WithEntryContent("remember me"))); err != nil {
		t.Fatal(err)
	}
	anchor, err := jevext.NewAnchor(entry.Seq{}, ownerID, jevext.Anchor{
		State: jevext.MemoryState{Decisions: []string{"remember me"}},
		SeqS:  entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tape.Store(ctx, anchor); err != nil {
		t.Fatal(err)
	}
	adapter, err := NewTapeAdapter(tape, "app-a")
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.SearchMemory(ctx, &memory.SearchRequest{AppName: "app-a", UserID: ownerID, Query: "remember"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Memories) != 1 || result.Memories[0].Content.Parts[0].Text != "remember me" {
		t.Fatalf("search memories = %#v", result.Memories)
	}
}

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
	payload, err := json.Marshal(entry.HandoffAnchor{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Tape.Store(ctx, entry.NewAnchor(entry.SeqFromUint64(2), ownerID, entry.AnchorKindHandoff, payload)); err != nil {
		t.Fatal(err)
	}
	adapter.Tape.SetView(view.EntryRange{SeqS: entry.SeqFromUint64(3)})
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
