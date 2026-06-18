package agent

import (
	"context"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/model"
	"google.golang.org/adk/session"

	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/spf13/afero"
)

func TestTapeAdapterSessionAndContextWindow(t *testing.T) {
	store, err := jsonl.NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	adapter, err := NewTapeAdapter(&tape.Tape{TapeStorage: store, OwnerID: "owner-a"}, "app-a")
	if err != nil {
		t.Fatal(err)
	}
	created, err := adapter.Create(ctx, &session.CreateRequest{
		AppName: "app-a", UserID: "owner-a", SessionID: "session-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := session.NewEvent("invocation-a")
	event.Author = "user"
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
	event.Author = "agent"
	event.LLMResponse = model.LLMResponse{Content: &genai.Content{
		Role: genai.RoleModel, Parts: []*genai.Part{{Text: "current"}},
	}}
	if err := adapter.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}

	got, err := adapter.Get(ctx, &session.GetRequest{
		AppName: "app-a", UserID: "owner-a", SessionID: "session-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Session.Events().Len() != 1 || got.Session.Events().At(0).Content.Parts[0].Text != "current" {
		t.Fatalf("session event mismatch: %#v", got.Session.Events())
	}

	req := &model.LLMRequest{}
	if _, err := adapter.ContextWindow(nil, req); err != nil {
		t.Fatal(err)
	}
	if len(req.Contents) != 1 || req.Contents[0].Parts[0].Text != "current" {
		t.Fatalf("context window mismatch: %#v", req.Contents)
	}
}
