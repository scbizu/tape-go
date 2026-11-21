package agent

import (
	"context"
	"iter"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scbizu/tape-go/pkg/ext/steer"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func TestSteerWithADKRunner(t *testing.T) {
	for _, persist := range []bool{false, true} {
		t.Run("persist="+strconv.FormatBool(persist), func(t *testing.T) {
			adapter, inbox := newSteerFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			ctx, delivery := WithSteerInbox(ctx, inbox, SteerOptions{PersistToTape: persist})
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
			script := steerTestModel(func(req *model.LLMRequest) *genai.Content {
				step++
				positions := steerPositions(req, "change direction")
				switch step {
				case 1:
					if len(positions) != 0 {
						t.Fatalf("unexpected steer in first request at %v", positions)
					}
					return toolCalls("first", "skip")
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
					return toolCalls("after")
				case 3:
					var want []int
					if persist {
						want = []int{steerIndex}
					}
					if !slices.Equal(positions, want) {
						t.Fatalf("steer at %v, want %v", positions, want)
					}
					return genai.NewContentFromText("redirected", genai.RoleModel)
				default:
					t.Fatalf("unexpected model step %d", step)
					return nil
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
			for _, err := range r.Run(ctx, adapter.Tape.OwnerID, "session", genai.NewContentFromText("begin", genai.RoleUser), adkagent.RunConfig{}) {
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
					if e.GetKind() != entry.EntryUser {
						t.Fatalf("persisted steer kind = %s", e.GetKind())
					}
				}
			}
			if persist && writes != 1 || !persist && writes != 0 {
				t.Fatalf("main tape writes of steer = %d, persist = %v", writes, persist)
			}
		})
	}
}

func TestSteerRetriesWithoutAck(t *testing.T) {
	adapter, inbox := newSteerFixture(t)
	ctx := t.Context()
	id, err := inbox.Enqueue(ctx, "redirect")
	if err != nil {
		t.Fatal(err)
	}
	checkWindow := func(ctx context.Context, want bool) {
		t.Helper()
		req := &model.LLMRequest{}
		if _, err := adapter.ContextWindow(steerCallbackContext{ctx: ctx}, req); err != nil {
			t.Fatal(err)
		}
		got := steerPositions(req, "redirect")
		if want && !slices.Equal(got, []int{len(req.Contents) - 1}) || !want && len(got) != 0 {
			t.Fatalf("steer positions = %v, expect delivery = %v", got, want)
		}
	}

	runCtx, first := WithSteerInbox(ctx, inbox, SteerOptions{})
	checkWindow(runCtx, true)
	checkWindow(runCtx, false)
	if !slices.Equal(first.Delivered(), []entry.Seq{id}) {
		t.Fatalf("first run delivered = %v, want %s", first.Delivered(), id)
	}

	retryCtx, retry := WithSteerInbox(ctx, inbox, SteerOptions{})
	checkWindow(retryCtx, true)
	if !slices.Equal(retry.Delivered(), []entry.Seq{id}) {
		t.Fatalf("retry delivered = %v, want %s", retry.Delivered(), id)
	}
	if err := inbox.Ack(ctx, id); err != nil {
		t.Fatal(err)
	}
	nextCtx, next := WithSteerInbox(ctx, inbox, SteerOptions{})
	checkWindow(nextCtx, false)
	if len(next.Delivered()) != 0 {
		t.Fatalf("acknowledged input was delivered: %v", next.Delivered())
	}
}

func newSteerFixture(t *testing.T) (*TapeAdapter, *steer.Inbox) {
	t.Helper()
	store, err := jsonl.NewJSONLStorage("session", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	for _, id := range []string{"main", "inbox"} {
		if err := store.Init(owner.WithOwnerId(t.Context(), id)); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := NewTapeAdapter(&tape.Tape{TapeStorage: store, OwnerID: "main"}, "app")
	if err != nil {
		t.Fatal(err)
	}
	inbox, err := steer.NewInbox(store, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	return adapter, inbox
}

type steerTestModel func(*model.LLMRequest) *genai.Content

func (steerTestModel) Name() string { return "steer-test-model" }

func (m steerTestModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{Content: m(req)}, nil)
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

type steerCallbackContext struct {
	adkagent.CallbackContext
	ctx context.Context
}

func (c steerCallbackContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c steerCallbackContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c steerCallbackContext) Err() error                  { return c.ctx.Err() }
func (c steerCallbackContext) Value(key any) any           { return c.ctx.Value(key) }
