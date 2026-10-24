// Package openai adapts OpenAI-compatible chat completions to ADK's model.LLM interface.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"

	"google.golang.org/genai"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/view"

	"google.golang.org/adk/model"
)

const (
	DefaultBaseURL = "https://api.openai.com/v1"
	DefaultModel   = "gpt-4o-mini"
)

// Model adapts the OpenAI Go SDK to ADK and Tape.
type Model struct {
	client *sdk.Client
	name   string
}

var _ model.LLM = (*Model)(nil)
var _ llm.Model = (*Model)(nil)
var _ llm.Summarizer = (*Model)(nil)

// ErrEmbeddingUnsupported indicates that this chat adapter has no embedding model.
var ErrEmbeddingUnsupported = errors.New("openai: embedding is not supported")

// NewModel creates a Chat Completions model. An empty modelName uses DefaultModel.
func NewModel(apiKey, modelName string, opts ...option.RequestOption) (*Model, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("openai: empty API key")
	}
	for _, opt := range opts {
		if opt == nil {
			return nil, errors.New("openai: nil option")
		}
	}
	options := []option.RequestOption{
		option.WithAPIKey(apiKey), option.WithBaseURL(DefaultBaseURL),
		option.WithRequestTimeout(2 * time.Minute), option.WithMaxRetries(0),
	}
	options = append(options, opts...)
	client := sdk.NewClient(options...)
	if strings.TrimSpace(modelName) == "" {
		modelName = DefaultModel
	}
	return &Model{client: &client, name: modelName}, nil
}

func (m *Model) Name() string {
	if m == nil {
		return ""
	}
	return m.name
}

func (m *Model) IsEnable() bool {
	return m != nil && m.client != nil && m.name != ""
}

func (m *Model) Embedding(context.Context, string) ([]float32, error) {
	return nil, ErrEmbeddingUnsupported
}

func (m *Model) ReRank(ctx context.Context, query string, candidates []string) ([]string, error) {
	if query == "" {
		return nil, errors.New("openai: empty rerank query")
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if !m.IsEnable() {
		return nil, errors.New("openai: model is not enabled")
	}
	body, err := json.Marshal(struct {
		Query      string   `json:"query"`
		Candidates []string `json:"candidates"`
	}{Query: query, Candidates: candidates})
	if err != nil {
		return nil, fmt.Errorf("openai: encode rerank request: %w", err)
	}
	resp, err := m.client.Chat.Completions.New(ctx, sdk.ChatCompletionNewParams{
		Model: m.name,
		Messages: []sdk.ChatCompletionMessageParamUnion{
			sdk.SystemMessage("You rerank candidates for a search query. Return JSON only: " +
				`{"order":[candidate_indexes_in_best_to_worst_order]}. ` +
				"Use zero-based indexes. Include every candidate exactly once.",
			), sdk.UserMessage(string(body)),
		},
		ResponseFormat: sdk.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}},
	})
	if err != nil {
		return nil, fmt.Errorf("openai: rerank: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("openai: rerank empty response")
	}
	return rerankByOrder(candidates, resp.Choices[0].Message.Content)
}

func (m *Model) Summarize(ctx context.Context, projection view.Projection) (llm.Summary, error) {
	if !m.IsEnable() {
		return llm.Summary{}, errors.New("openai: model is not enabled")
	}
	state, err := json.Marshal(projection)
	if err != nil {
		return llm.Summary{}, fmt.Errorf("openai: encode view projection: %w", err)
	}
	resp, err := m.client.Chat.Completions.New(ctx, sdk.ChatCompletionNewParams{
		Model: m.name,
		Messages: []sdk.ChatCompletionMessageParamUnion{
			sdk.SystemMessage("Summarize the supplied tape view faithfully and compactly as a JSON object for future retrieval. " +
				"Return one field: decisions (array of strings). Each decision must be a self-contained statement of durable information worth retrieving later, including concrete facts, constraints, preferences, results, or unfinished work when present. " +
				"Include at least one decision and preserve concrete names and values. " +
				"Do not infer or add information that is not present. Return JSON only.",
			), sdk.UserMessage(string(state)),
		},
		ResponseFormat: sdk.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}},
	})
	if err != nil {
		return llm.Summary{}, fmt.Errorf("openai: summarize: %w", err)
	}
	if len(resp.Choices) == 0 {
		return llm.Summary{}, errors.New("openai: summarize empty response")
	}
	if resp.Choices[0].FinishReason == "length" {
		return llm.Summary{}, errors.New("openai: summarize truncated at the output token limit")
	}
	summary := strings.TrimSpace(resp.Choices[0].Message.Content)
	if summary == "" {
		return llm.Summary{}, errors.New("openai: summarize empty content")
	}
	var memory llm.Summary
	if err := json.Unmarshal([]byte(summary), &memory); err != nil {
		return llm.Summary{}, fmt.Errorf("openai: summarize invalid summary: %w", err)
	}
	if memory.IsZero() {
		return llm.Summary{}, errors.New("openai: summarize returned no decision")
	}
	return memory, nil
}

func (m *Model) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if !m.IsEnable() {
			yield(nil, errors.New("openai: model is not enabled"))
			return
		}
		chatReq, err := buildRequest(m.name, req)
		if err != nil {
			yield(nil, err)
			return
		}
		if stream {
			m.generateStream(ctx, chatReq, yield)
			return
		}
		resp, err := m.client.Chat.Completions.New(ctx, *chatReq)
		if err != nil {
			yield(nil, fmt.Errorf("openai: generate content: %w", err))
			return
		}
		llmResp, err := convertResponse(resp)
		yield(llmResp, err)
	}
}

func (m *Model) generateStream(ctx context.Context, req *sdk.ChatCompletionNewParams, yield func(*model.LLMResponse, error) bool) {
	req.StreamOptions = sdk.ChatCompletionStreamOptionsParam{IncludeUsage: sdk.Bool(true)}
	stream := m.client.Chat.Completions.NewStreaming(ctx, *req)
	defer stream.Close()
	acc := sdk.ChatCompletionAccumulator{}
	var modelVersion string
	for stream.Next() {
		chunk := stream.Current()
		if chunk.Model != "" {
			modelVersion = chunk.Model
		}
		if !acc.AddChunk(chunk) {
			yield(nil, errors.New("openai: could not accumulate stream chunk"))
			return
		}
		for _, choice := range chunk.Choices {
			if choice.Index == 0 && choice.Delta.Content != "" {
				if !yield(&model.LLMResponse{
					Content:      genai.NewContentFromText(choice.Delta.Content, genai.RoleModel),
					ModelVersion: modelVersion, Partial: true,
				}, nil) {
					return
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		yield(nil, fmt.Errorf("openai: receive stream: %w", err))
		return
	}
	if len(acc.Choices) == 0 || acc.Choices[0].FinishReason == "" {
		yield(nil, fmt.Errorf("openai: incomplete stream: %w", io.ErrUnexpectedEOF))
		return
	}
	acc.Model = modelVersion
	response, err := convertResponse(&acc.ChatCompletion)
	yield(response, err)
}

func buildRequest(modelName string, req *model.LLMRequest) (*sdk.ChatCompletionNewParams, error) {
	if req == nil {
		return nil, errors.New("openai: nil LLM request")
	}
	if req.Model != "" {
		modelName = req.Model
	}
	out := &sdk.ChatCompletionNewParams{Model: modelName}
	if cfg := req.Config; cfg != nil {
		if cfg.SystemInstruction != nil {
			text, err := textContent(cfg.SystemInstruction)
			if err != nil {
				return nil, fmt.Errorf("openai: system instruction: %w", err)
			}
			out.Messages = append(out.Messages, sdk.SystemMessage(text))
		}
		if cfg.Temperature != nil {
			out.Temperature = sdk.Float(float64(*cfg.Temperature))
		}
		if cfg.TopP != nil {
			out.TopP = sdk.Float(float64(*cfg.TopP))
		}
		if cfg.PresencePenalty != nil {
			out.PresencePenalty = sdk.Float(float64(*cfg.PresencePenalty))
		}
		if cfg.FrequencyPenalty != nil {
			out.FrequencyPenalty = sdk.Float(float64(*cfg.FrequencyPenalty))
		}
		if cfg.MaxOutputTokens != 0 {
			out.MaxCompletionTokens = sdk.Int(int64(cfg.MaxOutputTokens))
		}
		if len(cfg.StopSequences) > 0 {
			out.Stop = sdk.ChatCompletionNewParamsStopUnion{OfStringArray: cfg.StopSequences}
		}
		if cfg.ResponseMIMEType == "application/json" {
			out.ResponseFormat = sdk.ChatCompletionNewParamsResponseFormatUnion{OfJSONObject: &shared.ResponseFormatJSONObjectParam{}}
		}
		var err error
		out.Tools, err = convertTools(cfg.Tools)
		if err != nil {
			return nil, err
		}
		out.ToolChoice = toolChoice(cfg.ToolConfig, &out.Tools)
	}
	for _, content := range req.Contents {
		messages, err := convertContent(content)
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, messages...)
	}
	return out, nil
}

func convertContent(content *genai.Content) ([]sdk.ChatCompletionMessageParamUnion, error) {
	if content == nil {
		return nil, nil
	}
	if content.Role != "" && content.Role != genai.RoleUser && content.Role != genai.RoleModel {
		return nil, fmt.Errorf("openai: unsupported content role %q", content.Role)
	}
	var text strings.Builder
	var calls []sdk.ChatCompletionMessageToolCallUnionParam
	var responses []sdk.ChatCompletionMessageParamUnion
	for _, part := range content.Parts {
		if part == nil || part.Thought {
			continue
		}
		switch {
		case part.Text != "":
			text.WriteString(part.Text)
		case part.FunctionCall != nil:
			call := part.FunctionCall
			args, err := json.Marshal(call.Args)
			if err != nil {
				return nil, fmt.Errorf("openai: encode function %q arguments: %w", call.Name, err)
			}
			calls = append(calls, sdk.ChatCompletionMessageToolCallUnionParam{OfFunction: &sdk.ChatCompletionMessageFunctionToolCallParam{
				ID: call.ID, Function: sdk.ChatCompletionMessageFunctionToolCallFunctionParam{Name: call.Name, Arguments: string(args)},
			}})
		case part.FunctionResponse != nil:
			response := part.FunctionResponse
			body, err := json.Marshal(response.Response)
			if err != nil {
				return nil, fmt.Errorf("openai: encode function %q response: %w", response.Name, err)
			}
			responses = append(responses, sdk.ToolMessage(string(body), response.ID))
		default:
			return nil, errors.New("openai: unsupported non-text content part")
		}
	}
	var messages []sdk.ChatCompletionMessageParamUnion
	if text.Len() > 0 || len(calls) > 0 {
		if content.Role == genai.RoleModel {
			messages = append(messages, sdk.ChatCompletionMessageParamUnion{OfAssistant: &sdk.ChatCompletionAssistantMessageParam{
				Content: sdk.ChatCompletionAssistantMessageParamContentUnion{OfString: sdk.String(text.String())}, ToolCalls: calls,
			}})
		} else {
			if len(calls) > 0 {
				return nil, errors.New("openai: function calls require model role")
			}
			messages = append(messages, sdk.UserMessage(text.String()))
		}
	}
	return append(messages, responses...), nil
}

func textContent(content *genai.Content) (string, error) {
	var text strings.Builder
	for _, part := range content.Parts {
		if part == nil || part.Thought {
			continue
		}
		if part.Text == "" {
			return "", errors.New("only text is supported")
		}
		text.WriteString(part.Text)
	}
	return text.String(), nil
}

func convertTools(tools []*genai.Tool) ([]sdk.ChatCompletionToolUnionParam, error) {
	var result []sdk.ChatCompletionToolUnionParam
	for _, source := range tools {
		if source == nil {
			continue
		}
		if len(source.FunctionDeclarations) == 0 {
			return nil, errors.New("openai: only function tools are supported")
		}
		for _, declaration := range source.FunctionDeclarations {
			parameters, err := functionParameters(declaration)
			if err != nil {
				return nil, err
			}
			result = append(result, sdk.ChatCompletionToolUnionParam{OfFunction: &sdk.ChatCompletionFunctionToolParam{
				Function: shared.FunctionDefinitionParam{Name: declaration.Name, Description: sdk.String(declaration.Description), Parameters: parameters},
			}})
		}
	}
	return result, nil
}

func functionParameters(declaration *genai.FunctionDeclaration) (map[string]any, error) {
	if declaration == nil {
		return nil, errors.New("openai: nil function declaration")
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
		return nil, fmt.Errorf("openai: encode function %q schema: %w", declaration.Name, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("openai: decode function %q schema: %w", declaration.Name, err)
	}
	normalizeSchema(raw)
	return raw, nil
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

func toolChoice(config *genai.ToolConfig, tools *[]sdk.ChatCompletionToolUnionParam) sdk.ChatCompletionToolChoiceOptionUnionParam {
	if config == nil || config.FunctionCallingConfig == nil {
		return sdk.ChatCompletionToolChoiceOptionUnionParam{}
	}
	fc := config.FunctionCallingConfig
	if len(fc.AllowedFunctionNames) > 0 {
		allowed := make(map[string]bool, len(fc.AllowedFunctionNames))
		for _, name := range fc.AllowedFunctionNames {
			allowed[name] = true
		}
		filtered := (*tools)[:0]
		for _, tool := range *tools {
			if tool.OfFunction != nil && allowed[tool.OfFunction.Function.Name] {
				filtered = append(filtered, tool)
			}
		}
		*tools = filtered
	}
	switch fc.Mode {
	case genai.FunctionCallingConfigModeNone:
		return sdk.ChatCompletionToolChoiceOptionUnionParam{OfAuto: sdk.String("none")}
	case genai.FunctionCallingConfigModeAny:
		if len(fc.AllowedFunctionNames) == 1 {
			return sdk.ToolChoiceOptionFunctionToolChoice(sdk.ChatCompletionNamedToolChoiceFunctionParam{Name: fc.AllowedFunctionNames[0]})
		}
		return sdk.ChatCompletionToolChoiceOptionUnionParam{OfAuto: sdk.String("required")}
	default:
		return sdk.ChatCompletionToolChoiceOptionUnionParam{OfAuto: sdk.String("auto")}
	}
}

func convertResponse(resp *sdk.ChatCompletion) (*model.LLMResponse, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return nil, errors.New("openai: empty response")
	}
	choice := resp.Choices[0]
	content := &genai.Content{Role: genai.RoleModel}
	if choice.Message.Content != "" {
		content.Parts = append(content.Parts, &genai.Part{Text: choice.Message.Content})
	}
	for _, call := range choice.Message.ToolCalls {
		if call.Type != "function" {
			return nil, fmt.Errorf("openai: unsupported tool call type %q", call.Type)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return nil, fmt.Errorf("openai: decode function %q arguments: %w", call.Function.Name, err)
		}
		content.Parts = append(content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Function.Name, Args: args}})
	}
	return &model.LLMResponse{
		Content: content, UsageMetadata: usage(resp.Usage), ModelVersion: resp.Model,
		FinishReason: finishReason(choice.FinishReason), TurnComplete: true,
	}, nil
}

func usage(value sdk.CompletionUsage) *genai.GenerateContentResponseUsageMetadata {
	return &genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount: int32(value.PromptTokens), CandidatesTokenCount: int32(value.CompletionTokens),
		TotalTokenCount: int32(value.TotalTokens), ThoughtsTokenCount: int32(value.CompletionTokensDetails.ReasoningTokens),
		CachedContentTokenCount: int32(value.PromptTokensDetails.CachedTokens),
	}
}

func finishReason(reason string) genai.FinishReason {
	switch reason {
	case "stop", "tool_calls":
		return genai.FinishReasonStop
	case "content_filter":
		return genai.FinishReasonSafety
	case "length":
		return genai.FinishReasonMaxTokens
	case "":
		return genai.FinishReasonUnspecified
	default:
		return genai.FinishReasonOther
	}
}

func rerankByOrder(candidates []string, content string) ([]string, error) {
	var payload struct {
		Order []int `json:"order"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		return nil, fmt.Errorf("openai: decode rerank response: %w", err)
	}
	out := make([]string, 0, len(candidates))
	used := make([]bool, len(candidates))
	for _, index := range payload.Order {
		if index < 0 || index >= len(candidates) || used[index] {
			continue
		}
		out = append(out, candidates[index])
		used[index] = true
	}
	for index, candidate := range candidates {
		if !used[index] {
			out = append(out, candidate)
		}
	}
	return out, nil
}
