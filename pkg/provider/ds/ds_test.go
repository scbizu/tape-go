package ds

import (
	"context"
	"io"
	"testing"

	deepseek "github.com/cohesion-org/deepseek-go"
	"google.golang.org/genai"

	"google.golang.org/adk/model"
)

type fakeClient struct {
	request       *deepseek.ChatCompletionRequest
	streamRequest *deepseek.StreamChatCompletionRequest
	response      *deepseek.ChatCompletionResponse
	stream        deepseek.ChatCompletionStream
}

func (f *fakeClient) CreateChatCompletion(_ context.Context, req *deepseek.ChatCompletionRequest) (*deepseek.ChatCompletionResponse, error) {
	f.request = req
	return f.response, nil
}

func (f *fakeClient) CreateChatCompletionStream(_ context.Context, req *deepseek.StreamChatCompletionRequest) (deepseek.ChatCompletionStream, error) {
	f.streamRequest = req
	return f.stream, nil
}

type fakeStream struct {
	responses []*deepseek.StreamChatCompletionResponse
	index     int
}

func (s *fakeStream) Recv() (*deepseek.StreamChatCompletionResponse, error) {
	if s.index == len(s.responses) {
		return nil, io.EOF
	}
	response := s.responses[s.index]
	s.index++
	return response, nil
}

func (*fakeStream) Close() error { return nil }

func TestModelGenerateContent(t *testing.T) {
	client := &fakeClient{response: &deepseek.ChatCompletionResponse{
		Model: "deepseek-test",
		Choices: []deepseek.Choice{{
			Message: deepseek.Message{ToolCalls: []deepseek.ToolCall{{
				ID: "call-1", Type: "function",
				Function: deepseek.ToolCallFunction{Name: "rewind", Arguments: `{"max_anchors":1}`},
			}}},
			FinishReason: "tool_calls",
		}},
		Usage: deepseek.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	}}
	llm := &Model{client: client, name: "deepseek-test"}
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("rewind", genai.RoleUser)},
		Config: &genai.GenerateContentConfig{
			SystemInstruction: genai.NewContentFromText("use tools", genai.RoleUser),
			Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{
				Name: "rewind", ParametersJsonSchema: map[string]any{
					"type": "object", "properties": map[string]any{"max_anchors": map[string]any{"type": "integer"}},
				},
			}}}},
		},
	}
	var got *model.LLMResponse
	for response, err := range llm.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		got = response
	}
	if client.request.Model != "deepseek-test" || len(client.request.Messages) != 2 || client.request.Messages[0].Role != deepseek.ChatMessageRoleSystem {
		t.Fatalf("unexpected request: %#v", client.request)
	}
	if len(client.request.Tools) != 1 || client.request.Tools[0].Function.Name != "rewind" || client.request.Tools[0].Function.Parameters.Properties["max_anchors"] == nil {
		t.Fatalf("unexpected tools: %#v", client.request.Tools)
	}
	if got == nil || len(got.Content.Parts) != 1 || got.Content.Parts[0].FunctionCall == nil {
		t.Fatalf("unexpected response: %#v", got)
	}
	call := got.Content.Parts[0].FunctionCall
	if call.ID != "call-1" || call.Name != "rewind" || call.Args["max_anchors"] != float64(1) {
		t.Fatalf("unexpected function call: %#v", call)
	}
	if got.UsageMetadata.TotalTokenCount != 5 || got.FinishReason != genai.FinishReasonStop {
		t.Fatalf("unexpected metadata: %#v", got)
	}
}

func TestModelGenerateContentStream(t *testing.T) {
	client := &fakeClient{stream: &fakeStream{responses: []*deepseek.StreamChatCompletionResponse{
		{Model: "deepseek-test", Choices: []deepseek.StreamChoices{{Index: 0, Delta: deepseek.StreamDelta{Role: "assistant", Content: "hel"}}}},
		{Model: "deepseek-test", Choices: []deepseek.StreamChoices{{Index: 0, Delta: deepseek.StreamDelta{Content: "lo"}, FinishReason: "stop"}}, Usage: &deepseek.StreamUsage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}},
	}}}
	llm := &Model{client: client, name: "deepseek-test"}
	var responses []*model.LLMResponse
	for response, err := range llm.GenerateContent(context.Background(), &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)},
	}, true) {
		if err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if client.streamRequest == nil || !client.streamRequest.Stream {
		t.Fatalf("unexpected stream request: %#v", client.streamRequest)
	}
	if len(responses) != 3 || !responses[0].Partial || !responses[1].Partial {
		t.Fatalf("unexpected stream responses: %#v", responses)
	}
	final := responses[2]
	if final.Partial || !final.TurnComplete || final.Content.Parts[0].Text != "hello" || final.UsageMetadata.TotalTokenCount != 2 {
		t.Fatalf("unexpected final response: %#v", final)
	}
}

func TestBuildRequestKeepsMatchedToolResponse(t *testing.T) {
	req, err := buildRequest("deepseek-test", &model.LLMRequest{Contents: []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			ID: "call-1", Name: "rewind", Args: map[string]any{"max_anchors": 1},
		}}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "call-1", Name: "rewind", Response: map[string]any{"ok": true},
		}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != deepseek.ChatMessageRoleAssistant || req.Messages[1].Role != deepseek.ChatMessageRoleTool {
		t.Fatalf("unexpected messages: %#v", req.Messages)
	}
}

func TestBuildRequestDropsDanglingToolResponse(t *testing.T) {
	req, err := buildRequest("deepseek-test", &model.LLMRequest{Contents: []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hi"}}},
		{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			ID: "call-1", Name: "rewind", Response: map[string]any{"ok": true},
		}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != deepseek.ChatMessageRoleUser {
		t.Fatalf("unexpected messages: %#v", req.Messages)
	}
}
