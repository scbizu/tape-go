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
	validator := turnValidator{}
	if err := validator.validateRequest(request); err != nil {
		return taperunner.Response{}, fmt.Errorf("agent: %w", err)
	}

	req, err := adkRequest(request)
	if err != nil {
		return taperunner.Response{}, err
	}

	modelResponse, err := collectADKResponse(ctx, r.model, req)
	if err != nil {
		return taperunner.Response{}, err
	}
	if err := validator.validateModelResponse(modelResponse); err != nil {
		return taperunner.Response{}, fmt.Errorf("agent: %w", err)
	}

	response, err := neutralResponse(modelResponse.Content)
	if err != nil {
		return taperunner.Response{}, err
	}
	if err := validator.validateResponse(response); err != nil {
		return taperunner.Response{}, fmt.Errorf("agent: %w", err)
	}
	return response, nil
}

func collectADKResponse(ctx context.Context, llm model.LLM, request *model.LLMRequest) (*model.LLMResponse, error) {
	var response *model.LLMResponse
	seen := false
	for next, err := range llm.GenerateContent(ctx, request, false) {
		if err != nil {
			return nil, fmt.Errorf("agent: run ADK turn: %w", err)
		}
		if seen {
			return nil, errors.New("agent: ADK model returned multiple non-streaming responses")
		}
		response = next
		seen = true
	}
	if !seen {
		return nil, errors.New("agent: ADK model returned no response")
	}
	return response, nil
}

func adkRequest(request taperunner.Request) (*model.LLMRequest, error) {
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
		for _, spec := range request.Tools {
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
	return genai.NewContentFromText(message.Text, genai.RoleUser), nil
}

func adkAssistantContent(message taperunner.Message) (*genai.Content, error) {
	if message.Text != "" {
		return genai.NewContentFromText(message.Text, genai.RoleModel), nil
	}

	parts := make([]*genai.Part, 0, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		args, err := decodeJSONObject(call.Arguments)
		if err != nil {
			return nil, fmt.Errorf("tool call %d arguments: %w", i, err)
		}
		parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: call.ID, Name: call.Name, Args: args,
		}})
	}
	return &genai.Content{Role: genai.RoleModel, Parts: parts}, nil
}

func adkToolContent(message taperunner.Message) (*genai.Content, error) {
	parts := make([]*genai.Part, 0, len(message.ToolResults))
	for i, result := range message.ToolResults {
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
	var text strings.Builder
	calls := make([]taperunner.ToolCall, 0)
	for i, part := range content.Parts {
		if part.FunctionCall == nil {
			text.WriteString(part.Text)
			continue
		}
		arguments, err := json.Marshal(part.FunctionCall.Args)
		if err != nil {
			return taperunner.Response{}, fmt.Errorf("agent: encode ADK tool call %d arguments: %w", i, err)
		}
		calls = append(calls, taperunner.ToolCall{
			ID: part.FunctionCall.ID, Name: part.FunctionCall.Name, Arguments: arguments,
		})
	}
	return taperunner.Response{Output: text.String(), ToolCalls: calls}, nil
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
