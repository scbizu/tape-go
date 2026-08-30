package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	taperunner "github.com/scbizu/tape-go/pkg/runner"
)

type ADKRunner struct {
	model model.LLM
}

func NewADKRunner(llm model.LLM) *ADKRunner {
	return &ADKRunner{model: llm}
}

func (r *ADKRunner) RunTurn(ctx context.Context, request taperunner.Request) (taperunner.Response, error) {
	req, err := adkRequest(request)
	if err != nil {
		return taperunner.Response{}, err
	}

	var response *model.LLMResponse
	for next, err := range r.model.GenerateContent(ctx, req, false) {
		if err != nil {
			return taperunner.Response{}, fmt.Errorf("agent: run ADK turn: %w", err)
		}
		if next == nil {
			return taperunner.Response{}, errors.New("agent: ADK model returned nil response")
		}
		if response != nil {
			return taperunner.Response{}, errors.New("agent: ADK model returned multiple non-streaming responses")
		}
		response = next
	}
	if response == nil {
		return taperunner.Response{}, errors.New("agent: ADK model returned no response")
	}
	if response.ErrorCode != "" || response.ErrorMessage != "" {
		return taperunner.Response{}, fmt.Errorf("agent: ADK model response error %q: %s", response.ErrorCode, response.ErrorMessage)
	}
	if response.Partial {
		return taperunner.Response{}, errors.New("agent: ADK model returned partial response for non-streaming turn")
	}
	return neutralResponse(response.Content)
}

func adkRequest(request taperunner.Request) (*model.LLMRequest, error) {
	if len(request.Messages) == 0 {
		return nil, errors.New("agent: runner request has no messages")
	}
	contents := make([]*genai.Content, 0, len(request.Messages))
	for i, message := range request.Messages {
		content, err := adkContent(message)
		if err != nil {
			return nil, fmt.Errorf("agent: runner message %d: %w", i, err)
		}
		contents = append(contents, content)
	}

	config := &genai.GenerateContentConfig{}
	if request.SystemInstruction != "" {
		config.SystemInstruction = genai.NewContentFromText(request.SystemInstruction, genai.RoleUser)
	}
	if len(request.Tools) > 0 {
		declarations := make([]*genai.FunctionDeclaration, 0, len(request.Tools))
		seen := make(map[string]struct{}, len(request.Tools))
		for i, spec := range request.Tools {
			if spec.Name == "" {
				return nil, fmt.Errorf("agent: runner tool %d has empty name", i)
			}
			if _, ok := seen[spec.Name]; ok {
				return nil, fmt.Errorf("agent: runner tool %q is duplicated", spec.Name)
			}
			seen[spec.Name] = struct{}{}
			declaration := &genai.FunctionDeclaration{Name: spec.Name, Description: spec.Description}
			if len(spec.InputSchema) > 0 {
				schema, err := decodeJSONObject(spec.InputSchema)
				if err != nil {
					return nil, fmt.Errorf("agent: runner tool %q input schema: %w", spec.Name, err)
				}
				declaration.ParametersJsonSchema = schema
			}
			declarations = append(declarations, declaration)
		}
		config.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
	}

	return &model.LLMRequest{Model: request.Model, Contents: contents, Config: config}, nil
}

func adkContent(message taperunner.Message) (*genai.Content, error) {
	switch message.Role {
	case taperunner.RoleUser:
		return adkUserContent(message)
	case taperunner.RoleAssistant:
		return adkAssistantContent(message)
	case taperunner.RoleTool:
		return adkToolContent(message)
	default:
		return nil, fmt.Errorf("unsupported role %q", message.Role)
	}
}

func adkUserContent(message taperunner.Message) (*genai.Content, error) {
	if message.Text == "" || len(message.ToolCalls) > 0 || len(message.ToolResults) > 0 {
		return nil, errors.New("user message must contain only text")
	}
	return genai.NewContentFromText(message.Text, genai.RoleUser), nil
}

func adkAssistantContent(message taperunner.Message) (*genai.Content, error) {
	if len(message.ToolResults) > 0 {
		return nil, errors.New("assistant message must contain exactly one of text or tool calls")
	}
	if message.Text != "" {
		if len(message.ToolCalls) > 0 {
			return nil, errors.New("assistant message must contain exactly one of text or tool calls")
		}
		return genai.NewContentFromText(message.Text, genai.RoleModel), nil
	}
	if len(message.ToolCalls) == 0 {
		return nil, errors.New("assistant message must contain exactly one of text or tool calls")
	}

	parts := make([]*genai.Part, 0, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		args, err := decodeJSONObject(call.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call %d arguments: %w", i, err)
		}
		if call.Name == "" {
			return nil, fmt.Errorf("tool call %d has empty name", i)
		}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: call.ID, Name: call.Name, Args: args,
		}})
	}
	return &genai.Content{Role: genai.RoleModel, Parts: parts}, nil
}

func adkToolContent(message taperunner.Message) (*genai.Content, error) {
	if message.Text != "" || len(message.ToolCalls) > 0 || len(message.ToolResults) == 0 {
		return nil, errors.New("tool message must contain only tool results")
	}

	parts := make([]*genai.Part, 0, len(message.ToolResults))
	for i, result := range message.ToolResults {
		if result.Name == "" {
			return nil, fmt.Errorf("tool result %d has empty name", i)
		}
		payload, err := decodeJSONValue(result.Result)
		if err != nil {
			return nil, fmt.Errorf("tool result %d payload: %w", i, err)
		}
		response, ok := payload.(map[string]any)
		if !ok {
			response = map[string]any{"output": payload}
		}
		parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID: result.ID, Name: result.Name, Response: response,
		}})
	}
	return &genai.Content{Role: genai.RoleUser, Parts: parts}, nil
}

func neutralResponse(content *genai.Content) (taperunner.Response, error) {
	if content == nil {
		return taperunner.Response{}, errors.New("agent: ADK model response has no content")
	}
	if content.Role != "" && content.Role != genai.RoleModel {
		return taperunner.Response{}, fmt.Errorf("agent: ADK model response has role %q", content.Role)
	}
	var text strings.Builder
	calls := make([]taperunner.ToolCall, 0)
	for i, part := range content.Parts {
		if part == nil {
			return taperunner.Response{}, fmt.Errorf("agent: ADK model response part %d is nil", i)
		}
		switch {
		case part.Text != "" && part.FunctionCall == nil:
			text.WriteString(part.Text)
		case part.FunctionCall != nil && part.Text == "":
			if part.FunctionCall.Name == "" {
				return taperunner.Response{}, fmt.Errorf("agent: ADK tool call %d has empty name", i)
			}
			arguments, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				return taperunner.Response{}, fmt.Errorf("agent: encode ADK tool call %d arguments: %w", i, err)
			}
			calls = append(calls, taperunner.ToolCall{
				ID: part.FunctionCall.ID, Name: part.FunctionCall.Name, Arguments: arguments,
			})
		default:
			return taperunner.Response{}, fmt.Errorf("agent: ADK model response part %d is unsupported or conflicting", i)
		}
	}

	output := text.String()
	if output == "" && len(calls) == 0 {
		return taperunner.Response{}, errors.New("agent: ADK model response is empty")
	}
	if output != "" && len(calls) > 0 {
		return taperunner.Response{}, errors.New("agent: ADK model response mixes final output and tool calls")
	}
	return taperunner.Response{Output: output, ToolCalls: calls}, nil
}

func decodeJSONObject(data json.RawMessage) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("expected JSON object")
	}
	return out, nil
}

func decodeJSONValue(data json.RawMessage) (any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

var _ taperunner.Runner = (*ADKRunner)(nil)
