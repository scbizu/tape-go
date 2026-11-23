package agent

import (
	"context"
	"errors"
	"iter"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scbizu/tape-go/pkg/ext/steer"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestSteerWithADKRunner(t *testing.T) {
	adapter, inbox := newSteerFixture(t, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ctx, delivery := WithSteerInbox(ctx, inbox)
	enqueued := make(chan struct{})
	var messageID entry.Seq
	var executed atomic.Int32
	work, err := functiontool.New(functiontool.Config{Name: "work"}, func(ctx tool.Context, _ struct{}) (map[string]any, error) {
		executed.Add(1)
		if ctx.FunctionCallID() == "first" {
			defer close(enqueued)
			var err error
			messageID, err = inbox.Enqueue(ctx, "change direction")
			if err != nil {
				return nil, err
			}
		}
		return map[string]any{"status": "done"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForEnqueue := func(ctx tool.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
		if ctx.FunctionCallID() == "skip" {
			select {
			case <-enqueued:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	}

	step := 0
	steerIndex := 0
	script := steerTestModel(func(req *model.LLMRequest) (*genai.Content, error) {
		step++
		positions := steerPositions(req, "change direction")
		switch step {
		case 1:
			if len(positions) != 0 {
				t.Fatalf("unexpected steer in first request at %v", positions)
			}
			return toolCalls("first", "skip"), nil
		case 2:
			steerIndex = len(req.Contents) - 1
			if !slices.Equal(positions, []int{steerIndex}) || steerIndex < 1 {
				t.Fatalf("steer at %v, want once at end of request", positions)
			}
			responses := make(map[string]map[string]any)
			for _, p := range req.Contents[steerIndex-1].Parts {
				if p.FunctionResponse != nil {
					responses[p.FunctionResponse.ID] = p.FunctionResponse.Response
				}
			}
			if responses["first"]["status"] != "done" || responses["skip"]["status"] != "skipped" || responses["skip"]["reason"] != "steer_pending" {
				t.Fatalf("tool responses before steer = %v", responses)
			}
			return toolCalls("after"), nil
		case 3:
			want := []int{steerIndex}
			if !slices.Equal(positions, want) {
				t.Fatalf("steer at %v, want %v", positions, want)
			}
			return genai.NewContentFromText("redirected", genai.RoleModel), nil
		default:
			t.Fatalf("unexpected model step %d", step)
			return nil, nil
		}
	})
	llm, err := llmagent.New(llmagent.Config{
		Name:                 "steer_test",
		Model:                script,
		Tools:                []tool.Tool{work},
		BeforeModelCallbacks: []llmagent.BeforeModelCallback{adapter.ContextWindow},
		BeforeToolCallbacks:  []llmagent.BeforeToolCallback{waitForEnqueue, BeforeSteerTool},
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: adapter.AppName, Agent: llm, SessionService: adapter})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range r.Run(ctx, adapter.Tape.OwnerID, steerMainSession, genai.NewContentFromText("begin", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if step != 3 || executed.Load() != 2 || messageID.IsZero() || !slices.Equal(delivery.Delivered(), []entry.Seq{messageID}) {
		t.Fatalf("model steps = %d, tool executions = %d, delivered = %v, enqueued = %s", step, executed.Load(), delivery.Delivered(), messageID)
	}
	pending, err := inbox.Pending(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != messageID {
		t.Fatalf("pending after run = %v, %v", pending, err)
	}
	if err := inbox.Ack(ctx, messageID); err != nil {
		t.Fatal(err)
	}
	entries, err := adapter.Tape.Range(adapter.tapeContext(ctx), view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(100)})
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, e := range entries.Raw {
		if e.GetSummary() == "change direction" {
			writes++
			checkSteerEntry(t, e, messageID)
		}
	}
	if writes != 1 {
		t.Fatalf("main tape writes of steer = %d, want 1", writes)
	}
}

func TestSteerRetriesWithoutAck(t *testing.T) {
	dir := t.TempDir()
	adapter, inbox := newSteerFixture(t, dir)
	ctx := t.Context()
	id, err := inbox.Enqueue(ctx, "redirect")
	if err != nil {
		t.Fatal(err)
	}
	work, err := functiontool.New(functiontool.Config{Name: "work"}, func(tool.Context, struct{}) (map[string]any, error) {
		return map[string]any{"status": "failed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	newRunner := func(adapter *TapeAdapter, script steerTestModel) *runner.Runner {
		t.Helper()
		llm, err := llmagent.New(llmagent.Config{
			Name:                 "steer_retry_test",
			Model:                script,
			Tools:                []tool.Tool{work},
			BeforeModelCallbacks: []llmagent.BeforeModelCallback{adapter.ContextWindow},
			BeforeToolCallbacks:  []llmagent.BeforeToolCallback{BeforeSteerTool},
		})
		if err != nil {
			t.Fatal(err)
		}
		r, err := runner.New(runner.Config{AppName: adapter.AppName, Agent: llm, SessionService: adapter})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	failure := errors.New("model interrupted after the tool result")
	step := 0
	firstRunner := newRunner(adapter, func(req *model.LLMRequest) (*genai.Content, error) {
		step++
		if got := steerPositions(req, "redirect"); !slices.Equal(got, []int{1}) {
			t.Fatalf("first run step %d steer positions = %v, want [1]", step, got)
		}
		if step == 1 {
			return toolCalls("failed"), nil
		}
		return nil, failure
	})
	runCtx, first := WithSteerInbox(ctx, inbox)
	var runErr error
	for _, err := range firstRunner.Run(runCtx, adapter.Tape.OwnerID, steerMainSession, genai.NewContentFromText("begin", genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			runErr = err
			break
		}
	}
	if !errors.Is(runErr, failure) || step != 2 || !slices.Equal(first.Delivered(), []entry.Seq{id}) {
		t.Fatalf("first run error = %v, steps = %d, delivered = %v", runErr, step, first.Delivered())
	}

	for _, tp := range []*tape.Tape{adapter.Tape, inbox.Tape()} {
		if err := tp.Close(); err != nil {
			t.Fatal(err)
		}
	}
	adapter, inbox = newSteerFixture(t, dir)
	retryRunner := newRunner(adapter, func(req *model.LLMRequest) (*genai.Content, error) {
		if got := steerPositions(req, "redirect"); !slices.Equal(got, []int{1, 4}) || len(req.Contents) != 5 {
			t.Fatalf("retry request has %d contents and steer positions %v, want 5 contents and positions [1 4]", len(req.Contents), got)
		}
		call := req.Contents[2].Parts[0].FunctionCall
		result := req.Contents[3].Parts[0].FunctionResponse
		if call == nil || call.ID != "failed" || result == nil || result.ID != call.ID || result.Response["status"] != "failed" {
			t.Fatalf("retry history between steer deliveries has call %v, response %v", call, result)
		}
		return genai.NewContentFromText("recovered", genai.RoleModel), nil
	})
	retryCtx, retry := WithSteerInbox(ctx, inbox)
	for _, err := range retryRunner.Run(retryCtx, adapter.Tape.OwnerID, steerMainSession, nil, adkagent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(retry.Delivered(), []entry.Seq{id}) {
		t.Fatalf("retry delivered = %v, want %s", retry.Delivered(), id)
	}

	entries, err := adapter.Tape.Range(adapter.tapeContext(ctx), view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(7)})
	if err != nil {
		t.Fatal(err)
	}
	// ADK gives function responses the user role. The adapter retains their
	// full payload, including the failure checked in the retry request above.
	wantKinds := []entry.EntryKind{entry.EntryUser, entry.EntryUser, entry.EntryToolCall, entry.EntryUser, entry.EntryUser, entry.EntryAssistant}
	if len(entries.Raw) != len(wantKinds) {
		t.Fatalf("main tape has %d records, want %d", len(entries.Raw), len(wantKinds))
	}
	for i, e := range entries.Raw {
		if e.GetKind() != wantKinds[i] || e.GetID() != entry.SeqFromUint64(uint64(i+1)) {
			t.Fatalf("main tape record %d = %+v, want kind %q, Seq %d", i, e, wantKinds[i], i+1)
		}
	}
	checkSteerEntry(t, entries.Raw[1], id)
	checkSteerEntry(t, entries.Raw[4], id)

	if err := inbox.Ack(ctx, id); err != nil {
		t.Fatal(err)
	}
	nextRunner := newRunner(adapter, func(req *model.LLMRequest) (*genai.Content, error) {
		if got := steerPositions(req, "redirect"); !slices.Equal(got, []int{1, 4}) {
			t.Fatalf("post-ack steer positions = %v, want [1 4]", got)
		}
		return genai.NewContentFromText("finished", genai.RoleModel), nil
	})
	nextCtx, next := WithSteerInbox(ctx, inbox)
	for _, err := range nextRunner.Run(nextCtx, adapter.Tape.OwnerID, steerMainSession, nil, adkagent.RunConfig{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(next.Delivered()) != 0 {
		t.Fatalf("acknowledged input was delivered: %v", next.Delivered())
	}
}

func checkSteerEntry(t *testing.T, e entry.EntryLike, id entry.Seq) {
	t.Helper()
	delivery, ok := e.(entry.CustomEntry)
	if !ok || delivery.GetKind() != entry.EntryUser ||
		delivery.Extensions["steer:id"] != id.String() || delivery.Extensions["steer:session"] != steerSidecarSession {
		t.Fatalf("steer record = %+v, want user entry referencing %s Seq %s", e, steerSidecarSession, id)
	}
}

const (
	steerMainSession    = "session"
	steerSidecarSession = "session:steer"
)

func newSteerFixture(t *testing.T, dir string) (*TapeAdapter, *steer.Inbox) {
	t.Helper()
	const ownerID = "u:alice"
	openTape := func(sessionID string) *tape.Tape {
		t.Helper()
		store, err := jsonl.NewJSONLStorage(sessionID, dir)
		if err != nil {
			t.Fatal(err)
		}
		tp := &tape.Tape{TapeStorage: store, OwnerID: ownerID}
		if err := tp.Init(owner.WithOwnerId(t.Context(), ownerID)); err != nil {
			t.Fatal(err)
		}
		return tp
	}
	adapter, err := NewTapeAdapter(openTape(steerMainSession), "app")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := steer.NewInbox(openTape(steerSidecarSession))
	if err != nil {
		t.Fatal(err)
	}
	return adapter, inbox
}

type steerTestModel func(*model.LLMRequest) (*genai.Content, error)

func (steerTestModel) Name() string { return "steer-test-model" }

func (m steerTestModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		content, err := m(req)
		if err != nil {
			yield(nil, err)
			return
		}
		yield(&model.LLMResponse{Content: content}, nil)
	}
}

func toolCalls(ids ...string) *genai.Content {
	content := &genai.Content{Role: genai.RoleModel}
	for _, id := range ids {
		content.Parts = append(content.Parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{ID: id, Name: "work", Args: map[string]any{}},
		})
	}
	return content
}

func steerPositions(req *model.LLMRequest, text string) []int {
	var positions []int
	for i, content := range req.Contents {
		if content.Role == genai.RoleUser && len(content.Parts) == 1 && content.Parts[0].Text == text {
			positions = append(positions, i)
		}
	}
	return positions
}
