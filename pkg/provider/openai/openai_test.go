package openai

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/view"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func newTestModel(t *testing.T, f transportFunc) *Model {
	t.Helper()
	m, err := NewModel("secret", "test-model", option.WithBaseURL("https://example.com/custom/v1/"), option.WithHTTPClient(f))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func request() *model.LLMRequest {
	return &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}}
}

func TestGenerateContentWireFormat(t *testing.T) {
	m := newTestModel(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://example.com/custom/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("unexpected request: %v", r)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "override" || body["temperature"] != float64(0) || body["max_completion_tokens"] != float64(42) || body["max_tokens"] != nil || body["stream_options"] != nil {
			t.Fatalf("body = %#v", body)
		}
		tools := body["tools"].([]any)
		params := tools[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
		if params["additionalProperties"] != false || params["type"] != "object" {
			t.Fatalf("parameters = %#v", params)
		}
		messages := body["messages"].([]any)
		if len(messages) != 4 || messages[0].(map[string]any)["role"] != "system" || messages[3].(map[string]any)["tool_call_id"] != "call-1" {
			t.Fatalf("messages = %#v", messages)
		}
		return response(200, `{"model":"snapshot","choices":[{"message":{"content":null,"tool_calls":[{"id":"call-2","type":"function","function":{"name":"lookup","arguments":"{\"key\":\"value\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`), nil
	})
	req := request()
	req.Model = "override"
	zero := float32(0)
	req.Config = &genai.GenerateContentConfig{
		Temperature: &zero, MaxOutputTokens: 42,
		SystemInstruction: genai.NewContentFromText("use tools", genai.RoleUser),
		Tools:             []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "lookup", ParametersJsonSchema: map[string]any{"type": "object", "additionalProperties": false}}}}},
	}
	req.Contents = append(req.Contents,
		&genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "lookup", Args: map[string]any{"key": "old"}}}}},
		&genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "call-1", Name: "lookup", Response: map[string]any{"result": "ok"}}}}},
	)
	count := 0
	for got, err := range m.GenerateContent(context.Background(), req, false) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if got.ModelVersion != "snapshot" || !got.TurnComplete || got.Partial || got.FinishReason != genai.FinishReasonStop || got.Content.Parts[0].FunctionCall.Args["key"] != "value" || got.UsageMetadata.CachedContentTokenCount != 3 || got.UsageMetadata.ThoughtsTokenCount != 2 {
			t.Fatalf("response = %#v", got)
		}
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
}

func TestStreamToolCallsAndUsage(t *testing.T) {
	closed := false
	m := newTestModel(t, func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["stream_options"].(map[string]any)["include_usage"] != true {
			t.Fatalf("body = %#v", body)
		}
		data := ": heartbeat\n\n" +
			"data: {\"model\":\"snapshot\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"key\\\":\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"value\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"total_tokens\":14,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\n" + "data: [DONE]\n\n"
		resp := response(200, data)
		resp.Body = &trackingBody{ReadCloser: resp.Body, closed: &closed}
		return resp, nil
	})
	var results []*model.LLMResponse
	for got, err := range m.GenerateContent(context.Background(), request(), true) {
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, got)
	}
	if !closed || len(results) != 2 || !results[0].Partial {
		t.Fatalf("closed = %v, results = %#v", closed, results)
	}
	final := results[1]
	if !final.TurnComplete || final.Partial || final.ModelVersion != "snapshot" || final.Content.Parts[0].Text != "hello" || final.Content.Parts[1].FunctionCall.Args["key"] != "value" || final.UsageMetadata.TotalTokenCount != 14 {
		t.Fatalf("final = %#v", final)
	}
}

type trackingBody struct {
	io.ReadCloser
	closed *bool
}

func (b *trackingBody) Close() error { *b.closed = true; return b.ReadCloser.Close() }

func TestStreamStopsAndClosesOnEarlyExit(t *testing.T) {
	closed := false
	m := newTestModel(t, func(*http.Request) (*http.Response, error) {
		r := response(200, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ninvalid")
		r.Body = &trackingBody{ReadCloser: r.Body, closed: &closed}
		return r, nil
	})
	for _, err := range m.GenerateContent(context.Background(), request(), true) {
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	if !closed {
		t.Fatal("stream body was not closed")
	}
}

func TestErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		stream     bool
		status     int
		want       error
	}{
		{name: "api", body: `{"error":{"message":"unauthorized"}}`, status: 401},
		{name: "malformed", body: `{`, status: 200},
		{name: "empty", body: `{"choices":[]}`, status: 200},
		{name: "bad-tool-json", body: `{"choices":[{"message":{"tool_calls":[{"function":{"name":"lookup","arguments":"{"}}]}}]}`, status: 200},
		{name: "truncated-stream", body: "data: {\"choices\":[]}\n\n", status: 200, stream: true, want: io.ErrUnexpectedEOF},
		{name: "stream-api-error", body: "data: {\"error\":{\"message\":\"failed\"}}\n\n", status: 200, stream: true},
		{name: "malformed-stream", body: "data: {\n\n", status: 200, stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel(t, func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil })
			var got error
			for _, err := range m.GenerateContent(context.Background(), request(), tc.stream) {
				if err != nil {
					got = err
				}
			}
			if got == nil {
				t.Fatal("expected error")
			}
			if tc.want != nil && !errors.Is(got, tc.want) {
				t.Fatalf("error = %v", got)
			}
			if tc.status == 401 {
				var apiErr *sdk.Error
				if !errors.As(got, &apiErr) || apiErr.StatusCode != 401 {
					t.Fatalf("error = %v", got)
				}
			}
		})
	}
}

func TestSummaryAndRerank(t *testing.T) {
	calls := 0
	m := newTestModel(t, func(r *http.Request) (*http.Response, error) {
		var body sdk.ChatCompletionNewParams
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ResponseFormat.OfJSONObject == nil {
			t.Fatalf("body = %#v", body)
		}
		calls++
		content := `{"decisions":["The region is Tokyo."]}`
		if calls == 2 {
			content = `{"order":[2,0]}`
		}
		encoded, _ := json.Marshal(content)
		return response(200, `{"choices":[{"message":{"content":`+string(encoded)+`},"finish_reason":"stop"}]}`), nil
	})
	summary, err := m.Summarize(context.Background(), view.Projection{Entries: []view.ProjectedEntry{{Summary: "The region is Tokyo."}}})
	if err != nil || len(summary.Decisions) != 1 {
		t.Fatalf("summary = %#v, %v", summary, err)
	}
	order, err := m.ReRank(context.Background(), "query", []string{"a", "b", "c"})
	if err != nil || strings.Join(order, ",") != "c,a,b" {
		t.Fatalf("order = %v, %v", order, err)
	}
}

func TestConfigurationAndValidation(t *testing.T) {
	if _, err := NewModel("key", "", nil); err == nil {
		t.Fatal("accepted nil option")
	}
	if _, err := NewModel("", ""); err == nil {
		t.Fatal("accepted empty key")
	}
	m, err := NewModel("key", "")
	if err != nil || m.Name() != DefaultModel || !m.IsEnable() {
		t.Fatalf("model = %#v, %v", m, err)
	}
	if _, err := m.Embedding(context.Background(), "text"); !errors.Is(err, ErrEmbeddingUnsupported) {
		t.Fatal(err)
	}
	var disabled *Model
	if disabled.IsEnable() || disabled.Name() != "" {
		t.Fatal("nil model enabled")
	}
	for _, err := range disabled.GenerateContent(context.Background(), request(), false) {
		if err == nil {
			t.Fatal("nil model accepted")
		}
	}
	for _, req := range []*model.LLMRequest{nil, {Contents: []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{{InlineData: &genai.Blob{MIMEType: "image/png", Data: []byte("image")}}}}}}} {
		for _, err := range m.GenerateContent(context.Background(), req, false) {
			if err == nil {
				t.Fatal("invalid input accepted")
			}
		}
	}
}

func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := newTestModel(t, func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	for _, err := range m.GenerateContent(ctx, request(), false) {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestToolChoiceAndSampling(t *testing.T) {
	temperature, topP, penalty := float32(.5), float32(.8), float32(0)
	req := request()
	req.Config = &genai.GenerateContentConfig{
		Temperature: &temperature, TopP: &topP, FrequencyPenalty: &penalty, PresencePenalty: &penalty,
		StopSequences: []string{"END"}, ResponseMIMEType: "application/json",
		Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
			{Name: "keep", Parameters: &genai.Schema{Type: genai.TypeObject, Properties: map[string]*genai.Schema{"value": {Type: genai.TypeString}}}},
			{Name: "drop"},
		}}},
		ToolConfig: &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: genai.FunctionCallingConfigModeAny, AllowedFunctionNames: []string{"keep"}}},
	}
	got, err := buildRequest("test", req)
	if err != nil {
		t.Fatal(err)
	}
	choice := got.ToolChoice.OfFunctionToolChoice
	if choice == nil || choice.Function.Name != "keep" || len(got.Tools) != 1 || got.Tools[0].OfFunction.Function.Name != "keep" || got.Tools[0].OfFunction.Function.Parameters["type"] != "object" || got.TopP.Value != float64(topP) || got.Temperature.Value != float64(temperature) || !got.FrequencyPenalty.Valid() || got.FrequencyPenalty.Value != 0 || got.ResponseFormat.OfJSONObject == nil || got.Stop.OfStringArray[0] != "END" {
		t.Fatalf("request = %#v", got)
	}
}

func TestSummaryRejectsInvalidResults(t *testing.T) {
	for _, body := range []string{
		`{"choices":[]}`,
		`{"choices":[{"message":{"content":""}}]}`,
		`{"choices":[{"message":{"content":"invalid"}}]}`,
		`{"choices":[{"message":{"content":"{\"decisions\":[]}"}}]}`,
		`{"choices":[{"message":{"content":"{\"decisions\":[\"fact\"]}"},"finish_reason":"length"}]}`,
	} {
		t.Run(body, func(t *testing.T) {
			m := newTestModel(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
			if _, err := m.Summarize(context.Background(), view.Projection{}); err == nil {
				t.Fatal("invalid summary accepted")
			}
		})
	}
}
