// Package ds adapts DeepSeek chat completions to ADK's model.LLM interface.
package ds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"sort"
	"strings"

	deepseek "github.com/cohesion-org/deepseek-go"
	"google.golang.org/genai"

	"google.golang.org/adk/model"
)

type chatClient interface {
	CreateChatCompletion(context.Context, *deepseek.ChatCompletionRequest) (*deepseek.ChatCompletionResponse, error)
	CreateChatCompletionStream(context.Context, *deepseek.StreamChatCompletionRequest) (deepseek.ChatCompletionStream, error)
}

type Model struct {
	client chatClient
	name   string
}

var _ model.LLM = (*Model)(nil)

func NewModel(apiKey, modelName string, opts ...deepseek.Option) (*Model, error) {
	if modelName == "" {
		modelName = deepseek.DeepSeekV4Pro
	}
	client, err := deepseek.NewClientWithOptions(apiKey, opts...)
	if err != nil {
		return nil, fmt.Errorf("ds: create client: %w", err)
	}
	return &Model{client: client, name: modelName}, nil
}

func (m *Model) Name() string { return m.name }

func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		chatReq, err := buildRequest(m.name, req)
		if err != nil {
			yield(nil, err)
			return
		}
		if stream {
			m.generateStream(ctx, chatReq, yield)
			return
		}
		resp, err := m.client.CreateChatCompletion(ctx, chatReq)
		if err != nil {
			yield(nil, fmt.Errorf("ds: generate content: %w", err))
			return
		}
		llmResp, err := convertResponse(resp)
		yield(llmResp, err)
	}
}

func (m *Model) generateStream(ctx context.Context, req *deepseek.ChatCompletionRequest, yield func(*model.LLMResponse, error) bool) {
	stream, err := m.client.CreateChatCompletionStream(ctx, &deepseek.StreamChatCompletionRequest{
		Model: req.Model, Messages: req.Messages, Stream: true,
		FrequencyPenalty: req.FrequencyPenalty, MaxTokens: req.MaxTokens,
		PresencePenalty: req.PresencePenalty, Temperature: req.Temperature,
		TopP: req.TopP, Stop: req.Stop, Tools: req.Tools, ToolChoice: req.ToolChoice,
		ResponseFormat: req.ResponseFormat, StreamOptions: deepseek.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		yield(nil, fmt.Errorf("ds: generate stream: %w", err))
		return
	}
	defer stream.Close()

	var text strings.Builder
	calls := make(map[int]*deepseek.ToolCall)
	var usage *deepseek.StreamUsage
	var finish string
	var modelVersion string
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			yield(nil, fmt.Errorf("ds: receive stream: %w", err))
			return
		}
		modelVersion = chunk.Model
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				finish = choice.FinishReason
			}
			if choice.Delta.Content != "" {
				text.WriteString(choice.Delta.Content)
				if !yield(&model.LLMResponse{
					Content:      genai.NewContentFromText(choice.Delta.Content, genai.RoleModel),
					ModelVersion: modelVersion,
					Partial:      true,
				}, nil) {
					return
				}
			}
			mergeToolCalls(calls, choice.Delta.ToolCalls)
		}
	}
	content, err := contentFromMessage(deepseek.Message{Content: text.String(), ToolCalls: sortedToolCalls(calls)})
	if err != nil {
		yield(nil, err)
		return
	}
	yield(&model.LLMResponse{
		Content:       content,
		UsageMetadata: streamUsage(usage),
		ModelVersion:  modelVersion,
		FinishReason:  finishReason(finish),
		TurnComplete:  true,
	}, nil)
}

func buildRequest(modelName string, req *model.LLMRequest) (*deepseek.ChatCompletionRequest, error) {
	if req == nil {
		return nil, errors.New("ds: nil LLM request")
	}
	name := modelName
	if req.Model != "" {
		name = req.Model
	}
	out := &deepseek.ChatCompletionRequest{Model: name}
	if req.Config != nil {
		cfg := req.Config
		if cfg.SystemInstruction != nil {
			text, err := textContent(cfg.SystemInstruction)
			if err != nil {
				return nil, fmt.Errorf("ds: system instruction: %w", err)
			}
			out.Messages = append(out.Messages, deepseek.ChatCompletionMessage{Role: deepseek.ChatMessageRoleSystem, Content: text})
		}
		if cfg.Temperature != nil {
			out.Temperature = *cfg.Temperature
		}
		if cfg.TopP != nil {
			out.TopP = *cfg.TopP
		}
		if cfg.PresencePenalty != nil {
			out.PresencePenalty = *cfg.PresencePenalty
		}
		if cfg.FrequencyPenalty != nil {
			out.FrequencyPenalty = *cfg.FrequencyPenalty
		}
		out.MaxTokens = int(cfg.MaxOutputTokens)
		out.Stop = cfg.StopSequences
		if cfg.ResponseMIMEType == "application/json" {
			out.ResponseFormat = &deepseek.ResponseFormat{Type: "json_object"}
		}
		tools, err := convertTools(cfg.Tools)
		if err != nil {
			return nil, err
		}
		out.Tools = tools
		out.ToolChoice = toolChoice(cfg.ToolConfig, &out.Tools)
	}
	for _, content := range req.Contents {
		messages, err := convertContent(content)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, messages...)
	}
	out.Messages = validToolMessages(out.Messages)
	return out, nil
}

func validToolMessages(messages []deepseek.ChatCompletionMessage) []deepseek.ChatCompletionMessage {
	out := messages[:0]
	var pending map[string]bool
	for _, message := range messages {
		if message.Role == deepseek.ChatMessageRoleTool {
			if pending != nil && pending[message.ToolCallID] {
				out = append(out, message)
				delete(pending, message.ToolCallID)
			}
			continue
		}
		out = append(out, message)
		pending = nil
		if message.Role == deepseek.ChatMessageRoleAssistant && len(message.ToolCalls) > 0 {
			pending = make(map[string]bool, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if call.ID != "" {
					pending[call.ID] = true
				}
			}
		}
	}
	return out
}

func convertContent(content *genai.Content) ([]deepseek.ChatCompletionMessage, error) {
	if content == nil {
		return nil, nil
	}
	role := deepseek.ChatMessageRoleUser
	if content.Role == genai.RoleModel {
		role = deepseek.ChatMessageRoleAssistant
	} else if content.Role != "" && content.Role != genai.RoleUser {
		return nil, fmt.Errorf("ds: unsupported content role %q", content.Role)
	}
	message := deepseek.ChatCompletionMessage{Role: role}
	var toolMessages []deepseek.ChatCompletionMessage
	for _, part := range content.Parts {
		if part == nil || part.Thought {
			continue
		}
		switch {
		case part.Text != "":
			message.Content += part.Text
		case part.FunctionCall != nil:
			args, err := json.Marshal(part.FunctionCall.Args)
			if err != nil {
				return nil, fmt.Errorf("ds: encode function %q arguments: %w", part.FunctionCall.Name, err)
			}
			message.ToolCalls = append(message.ToolCalls, deepseek.ToolCall{
				ID: part.FunctionCall.ID, Type: "function",
				Function: deepseek.ToolCallFunction{Name: part.FunctionCall.Name, Arguments: string(args)},
			})
		case part.FunctionResponse != nil:
			body, err := json.Marshal(part.FunctionResponse.Response)
			if err != nil {
				return nil, fmt.Errorf("ds: encode function %q response: %w", part.FunctionResponse.Name, err)
			}
			toolMessages = append(toolMessages, deepseek.ChatCompletionMessage{
				Role: deepseek.ChatMessageRoleTool, Content: string(body), ToolCallID: part.FunctionResponse.ID,
			})
		default:
			return nil, errors.New("ds: unsupported non-text content part")
		}
	}
	var messages []deepseek.ChatCompletionMessage
	if message.Content != "" || len(message.ToolCalls) > 0 {
		messages = append(messages, message)
	}
	return append(messages, toolMessages...), nil
}

func textContent(content *genai.Content) (string, error) {
	messages, err := convertContent(&genai.Content{Role: genai.RoleUser, Parts: content.Parts})
	if err != nil {
		return "", err
	}
	if len(messages) != 1 || messages[0].Role != deepseek.ChatMessageRoleUser || len(messages[0].ToolCalls) != 0 {
		return "", errors.New("only text is supported")
	}
	return messages[0].Content, nil
}

func convertTools(tools []*genai.Tool) ([]deepseek.Tool, error) {
	var result []deepseek.Tool
	for _, source := range tools {
		if source == nil {
			continue
		}
		if len(source.FunctionDeclarations) == 0 {
			return nil, errors.New("ds: only function tools are supported")
		}
		for _, declaration := range source.FunctionDeclarations {
			parameters, err := functionParameters(declaration)
			if err != nil {
				return nil, err
			}
			result = append(result, deepseek.Tool{Type: "function", Function: deepseek.Function{
				Name: declaration.Name, Description: declaration.Description, Parameters: parameters,
			}})
		}
	}
	return result, nil
}

func functionParameters(declaration *genai.FunctionDeclaration) (*deepseek.FunctionParameters, error) {
	if declaration == nil {
		return nil, errors.New("ds: nil function declaration")
	}
	schema := declaration.ParametersJsonSchema
	if schema == nil {
		schema = declaration.Parameters
	}
	if schema == nil {
		return nil, nil
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("ds: encode function %q schema: %w", declaration.Name, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("ds: decode function %q schema: %w", declaration.Name, err)
	}
	normalizeSchema(raw)
	params := &deepseek.FunctionParameters{Type: "object"}
	if value, ok := raw["type"].(string); ok {
		params.Type = value
	}
	if value, ok := raw["properties"].(map[string]any); ok {
		params.Properties = value
	}
	if values, ok := raw["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				params.Required = append(params.Required, name)
			}
		}
	}
	return params, nil
}

func normalizeSchema(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "type" {
				if kind, ok := child.(string); ok {
					value[key] = strings.ToLower(kind)
				}
			}
			normalizeSchema(child)
		}
	case []any:
		for _, child := range value {
			normalizeSchema(child)
		}
	}
}

func toolChoice(config *genai.ToolConfig, tools *[]deepseek.Tool) any {
	if config == nil || config.FunctionCallingConfig == nil {
		return nil
	}
	fc := config.FunctionCallingConfig
	if len(fc.AllowedFunctionNames) > 0 {
		allowed := make(map[string]bool, len(fc.AllowedFunctionNames))
		for _, name := range fc.AllowedFunctionNames {
			allowed[name] = true
		}
		filtered := (*tools)[:0]
		for _, candidate := range *tools {
			if allowed[candidate.Function.Name] {
				filtered = append(filtered, candidate)
			}
		}
		*tools = filtered
	}
	switch fc.Mode {
	case genai.FunctionCallingConfigModeNone:
		return "none"
	case genai.FunctionCallingConfigModeAny:
		if len(fc.AllowedFunctionNames) == 1 {
			return deepseek.ToolChoice{Type: "function", Function: deepseek.ToolChoiceFunction{Name: fc.AllowedFunctionNames[0]}}
		}
		return "required"
	default:
		return "auto"
	}
}

func convertResponse(resp *deepseek.ChatCompletionResponse) (*model.LLMResponse, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return nil, errors.New("ds: empty response")
	}
	choice := resp.Choices[0]
	content, err := contentFromMessage(choice.Message)
	if err != nil {
		return nil, err
	}
	result := &model.LLMResponse{
		Content: content, UsageMetadata: usage(resp.Usage), ModelVersion: resp.Model,
		FinishReason: finishReason(choice.FinishReason), TurnComplete: true,
	}
	if choice.Message.ReasoningContent != "" {
		result.CustomMetadata = map[string]any{"reasoning_content": choice.Message.ReasoningContent}
	}
	return result, nil
}

func contentFromMessage(message deepseek.Message) (*genai.Content, error) {
	content := &genai.Content{Role: genai.RoleModel}
	if message.Content != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: message.Content})
	}
	for _, call := range message.ToolCalls {
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("ds: decode function %q arguments: %w", call.Function.Name, err)
		}
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{
			ID: call.ID, Name: call.Function.Name, Args: args,
		}})
	}
	return content, nil
}

func mergeToolCalls(dst map[int]*deepseek.ToolCall, calls []deepseek.ToolCall) {
	for _, part := range calls {
		call := dst[part.Index]
		if call == nil {
			call = &deepseek.ToolCall{Index: part.Index}
			dst[part.Index] = call
		}
		if part.ID != "" {
			call.ID = part.ID
		}
		if part.Type != "" {
			call.Type = part.Type
		}
		if part.Function.Name != "" {
			call.Function.Name = part.Function.Name
		}
		call.Function.Arguments += part.Function.Arguments
	}
}

func sortedToolCalls(calls map[int]*deepseek.ToolCall) []deepseek.ToolCall {
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	result := make([]deepseek.ToolCall, 0, len(indexes))
	for _, index := range indexes {
		result = append(result, *calls[index])
	}
	return result
}

func usage(value deepseek.Usage) *genai.GenerateContentResponseUsageMetadata {
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: int32(value.PromptTokens), CandidatesTokenCount: int32(value.CompletionTokens),
		TotalTokenCount: int32(value.TotalTokens), ThoughtsTokenCount: int32(value.CompletionTokensDetails.ReasoningTokens),
		CachedContentTokenCount: int32(value.PromptCacheHitTokens),
	}
}

func streamUsage(value *deepseek.StreamUsage) *genai.GenerateContentResponseUsageMetadata {
	if value == nil {
		return nil
	}
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: int32(value.PromptTokens), CandidatesTokenCount: int32(value.CompletionTokens),
		TotalTokenCount: int32(value.TotalTokens), ThoughtsTokenCount: int32(value.CompletionTokensDetails.ReasoningTokens),
		CachedContentTokenCount: int32(value.PromptCacheHitTokens),
	}
}

func finishReason(reason string) genai.FinishReason {
	switch reason {
	case "stop", "tool_calls":
		return genai.FinishReasonStop
	case "length":
		return genai.FinishReasonMaxTokens
	case "":
		return genai.FinishReasonUnspecified
	default:
		return genai.FinishReasonOther
	}
}
