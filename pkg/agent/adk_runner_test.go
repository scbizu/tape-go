package agent

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	taperunner "github.com/scbizu/tape-go/pkg/runner"
)

func TestADKRunnerReturnsFinalOutput(t *testing.T) {
	t.Parallel()

	model := &fakeADKModel{responses: []*model.LLMResponse{{
		Content: genai.NewContentFromText("done", genai.RoleModel),
	}}}
	runner := NewADKRunner(model)

	response, err := runner.RunTurn(context.Background(), taperunner.Request{
		SystemInstruction: "be concise",
		Messages:          []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Output != "done" || len(response.ToolCalls) != 0 {
		t.Fatalf("response mismatch: %#v", response)
	}
	if model.calls != 1 || model.lastRequest == nil {
		t.Fatalf("model calls = %d, request = %#v", model.calls, model.lastRequest)
	}
	if got := model.lastRequest.Config.SystemInstruction.Parts[0].Text; got != "be concise" {
		t.Fatalf("system instruction = %q, want be concise", got)
	}
}

func TestADKRunnerReturnsToolCallsWithoutExecutingThem(t *testing.T) {
	t.Parallel()

	model := &fakeADKModel{responses: []*model.LLMResponse{{Content: &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call-1", Name: "search", Args: map[string]any{"query": "tape"},
		}}},
	}}}}
	runner := NewADKRunner(model)

	response, err := runner.RunTurn(context.Background(), taperunner.Request{
		Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "find it"}},
		Tools: []taperunner.ToolSpec{{
			Name:        "search",
			Description: "search memory",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Output != "" || len(response.ToolCalls) != 1 {
		t.Fatalf("response mismatch: %#v", response)
	}
	call := response.ToolCalls[0]
	if call.ID != "call-1" || call.Name != "search" || string(call.Arguments) != `{"query":"tape"}` {
		t.Fatalf("tool call mismatch: %#v", call)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
}

func TestADKRunnerConvertsToolResultsIntoNextTurn(t *testing.T) {
	t.Parallel()

	model := &fakeADKModel{responses: []*model.LLMResponse{{
		Content: genai.NewContentFromText("final", genai.RoleModel),
	}}}
	runner := NewADKRunner(model)

	_, err := runner.RunTurn(context.Background(), taperunner.Request{Messages: []taperunner.Message{
		{Role: taperunner.RoleAssistant, ToolCalls: []taperunner.ToolCall{{
			ID: "call-1", Name: "search", Arguments: json.RawMessage(`{"query":"tape"}`),
		}}},
		{Role: taperunner.RoleTool, ToolResults: []taperunner.ToolResult{{
			ID: "call-1", Name: "search", Result: json.RawMessage(`{"items":["one"]}`),
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	call := model.lastRequest.Contents[0].Parts[0].FunctionCall
	if call == nil || call.ID != "call-1" || call.Name != "search" || call.Args["query"] != "tape" {
		t.Fatalf("function call request mismatch: %#v", call)
	}
	result := model.lastRequest.Contents[1].Parts[0].FunctionResponse
	if result == nil || result.ID != "call-1" || result.Name != "search" {
		t.Fatalf("function result request mismatch: %#v", result)
	}
}

func TestADKRunnerRejectsMixedAndEmptyResponses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response *model.LLMResponse
	}{
		{name: "empty", response: &model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel}}},
		{name: "mixed", response: &model.LLMResponse{Content: &genai.Content{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{Text: "working"},
				{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "search", Args: map[string]any{}}},
			},
		}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := NewADKRunner(&fakeADKModel{responses: []*model.LLMResponse{test.response}})
			if _, err := runner.RunTurn(context.Background(), taperunner.Request{
				Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
			}); err == nil {
				t.Fatal("RunTurn() error = nil, want response validation error")
			}
		})
	}
}

func TestADKRunnerPropagatesModelError(t *testing.T) {
	t.Parallel()

	want := errors.New("model unavailable")
	runner := NewADKRunner(&fakeADKModel{err: want})
	_, err := runner.RunTurn(context.Background(), taperunner.Request{
		Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
	})
	if !errors.Is(err, want) {
		t.Fatalf("RunTurn() error = %v, want %v", err, want)
	}
}

func TestADKRunnerValidatesRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request taperunner.Request
	}{
		{name: "no messages"},
		{name: "unsupported role", request: taperunner.Request{
			Messages: []taperunner.Message{{Role: "system", Text: "hello"}},
		}},
		{name: "mixed message payload", request: taperunner.Request{
			Messages: []taperunner.Message{{
				Role: taperunner.RoleAssistant, Text: "hello",
				ToolCalls: []taperunner.ToolCall{{Name: "search"}},
			}},
		}},
		{name: "invalid tool schema", request: taperunner.Request{
			Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
			Tools:    []taperunner.ToolSpec{{Name: "search", InputSchema: json.RawMessage(`{`)}},
		}},
		{name: "non-object tool schema", request: taperunner.Request{
			Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
			Tools:    []taperunner.ToolSpec{{Name: "search", InputSchema: json.RawMessage(`true`)}},
		}},
		{name: "duplicate tool", request: taperunner.Request{
			Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
			Tools: []taperunner.ToolSpec{
				{Name: "search"},
				{Name: "search"},
			},
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := NewADKRunner(&fakeADKModel{responses: []*model.LLMResponse{{
				Content: genai.NewContentFromText("done", genai.RoleModel),
			}}})
			if _, err := runner.RunTurn(context.Background(), test.request); err == nil {
				t.Fatal("RunTurn() error = nil, want request validation error")
			}
		})
	}
}

func TestADKRunnerRejectsInvalidModelSequence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []*model.LLMResponse
	}{
		{name: "no response"},
		{name: "nil response", responses: []*model.LLMResponse{nil}},
		{name: "multiple responses", responses: []*model.LLMResponse{
			{Content: genai.NewContentFromText("one", genai.RoleModel)},
			{Content: genai.NewContentFromText("two", genai.RoleModel)},
		}},
		{name: "partial response", responses: []*model.LLMResponse{{
			Content: genai.NewContentFromText("partial", genai.RoleModel), Partial: true,
		}}},
		{name: "response error", responses: []*model.LLMResponse{{
			ErrorCode: "unavailable", ErrorMessage: "try later",
		}}},
		{name: "wrong response role", responses: []*model.LLMResponse{{
			Content: genai.NewContentFromText("not a model response", genai.RoleUser),
		}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := NewADKRunner(&fakeADKModel{responses: test.responses})
			if _, err := runner.RunTurn(context.Background(), taperunner.Request{
				Messages: []taperunner.Message{{Role: taperunner.RoleUser, Text: "hello"}},
			}); err == nil {
				t.Fatal("RunTurn() error = nil, want model sequence error")
			}
		})
	}
}

type fakeADKModel struct {
	responses   []*model.LLMResponse
	err         error
	calls       int
	lastRequest *model.LLMRequest
}

func (*fakeADKModel) Name() string { return "fake" }

func (m *fakeADKModel) GenerateContent(_ context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		m.lastRequest = req
		if stream {
			yield(nil, errors.New("unexpected streaming request"))
			return
		}
		if m.err != nil {
			yield(nil, m.err)
			return
		}
		for _, response := range m.responses {
			if !yield(response, nil) {
				return
			}
		}
	}
}

var _ model.LLM = (*fakeADKModel)(nil)
