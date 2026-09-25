// Package jev adapts TypeSafe AI's Jev API to finder.JevClassifier.
package jev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/finder"
)

const (
	DefaultEndpoint = "https://api.typesafe.ai/v1/systemone"
	DefaultModel    = "jev-latest"
)

var _ finder.JevClassifier = (*Client)(nil)
var _ finder.JevAnchorDecider = (*Client)(nil)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	apiKey     string
	endpoint   string
	model      string
	httpClient *retryablehttp.Client
}

type Option func(*Client) error

func WithEndpoint(endpoint string) Option {
	return func(client *Client) error {
		if strings.TrimSpace(endpoint) == "" {
			return errors.New("jev: empty endpoint")
		}
		client.endpoint = endpoint
		return nil
	}
}

func WithModel(model string) Option {
	return func(client *Client) error {
		if strings.TrimSpace(model) == "" {
			return errors.New("jev: empty model")
		}
		client.model = model
		return nil
	}
}

func WithHTTPClient(httpClient HTTPClient) Option {
	return func(client *Client) error {
		if httpClient == nil {
			return errors.New("jev: nil HTTP client")
		}
		client.httpClient.HTTPClient = &http.Client{
			Transport: httpClientTransport{client: httpClient},
			Timeout:   30 * time.Second,
		}
		return nil
	}
}

func WithMaxRetries(maxRetries int) Option {
	return func(client *Client) error {
		if maxRetries < 0 {
			return errors.New("jev: max retries cannot be negative")
		}
		client.httpClient.RetryMax = maxRetries
		return nil
	}
}

func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("jev: empty API key")
	}
	retryClient := retryablehttp.NewClient()
	retryClient.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	retryClient.Logger = nil
	retryClient.RetryWaitMin = 250 * time.Millisecond
	retryClient.RetryWaitMax = 2 * time.Second
	retryClient.RetryMax = 2
	retryClient.CheckRetry = checkRetry
	retryClient.Backoff = retryablehttp.DefaultBackoff
	retryClient.ErrorHandler = retryablehttp.PassthroughErrorHandler
	client := &Client{
		apiKey:     apiKey,
		endpoint:   DefaultEndpoint,
		model:      DefaultModel,
		httpClient: retryClient,
	}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(client); err != nil {
			return nil, err
		}
	}
	return client, nil
}

type candidateState struct {
	ID    string               `json:"id"`
	State entry.JevMemoryState `json:"state"`
}

type question struct {
	Type         string   `json:"type"`
	Instructions string   `json:"instructions"`
	Criteria     []string `json:"criteria"`
}

type noulQuestion struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type classifyRequest struct {
	State struct {
		Query      string           `json:"query"`
		Candidates []candidateState `json:"candidates"`
	} `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]question `json:"questions"`
}

type scoreAnswer struct {
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

type classifyResponse struct {
	Model   string                 `json:"model"`
	Answers map[string]scoreAnswer `json:"answers"`
}

type anchorResponse struct {
	Answers map[string]struct {
		Noul float64 `json:"noul"`
	} `json:"answers"`
}

// ShouldAnchor asks Jev whether one entry contains durable information worth
// exposing as a future memory-search candidate.
func (c *Client) ShouldAnchor(ctx context.Context, projection finder.JevViewProjection) (float64, error) {
	if c == nil || c.httpClient == nil {
		return 0, errors.New("jev: client is not enabled")
	}
	if len(projection.Entries) == 0 {
		return 0, errors.New("jev: empty anchor view projection")
	}
	payload := struct {
		State     finder.JevViewProjection `json:"state"`
		Model     string                   `json:"model"`
		Questions map[string]noulQuestion  `json:"questions"`
	}{
		State: projection,
		Model: c.model,
		Questions: map[string]noulQuestion{
			"should_anchor": {
				Type:         "noul",
				Instructions: "Should this event be retained as a durable memory point for accurately answering future questions?",
				Criteria: map[string]string{
					"true":  "Contains a durable fact, decision, constraint, preference, result, or unresolved task likely to matter later",
					"false": "Transient conversation, repetition, acknowledgement, or information unlikely to help a future task",
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("jev: encode anchor decision: %w", err)
	}
	var response anchorResponse
	if err := c.post(ctx, body, &response); err != nil {
		return 0, err
	}
	answer, ok := response.Answers["should_anchor"]
	if !ok {
		return 0, errors.New("jev: response missing answer \"should_anchor\"")
	}
	return answer.Noul, nil
}

// ValidateSummary asks Jev whether a generated summary is fully supported by
// its source view and preserves the durable information needed for retrieval.
func (c *Client) ValidateSummary(ctx context.Context, projection finder.JevViewProjection, summary entry.JevMemoryState) (float64, error) {
	if c == nil || c.httpClient == nil {
		return 0, errors.New("jev: client is not enabled")
	}
	if len(projection.Entries) == 0 || summary.IsZero() {
		return 0, errors.New("jev: summary validation requires state and summary")
	}
	payload := struct {
		State struct {
			SourceView      finder.JevViewProjection `json:"source_view"`
			ProposedSummary entry.JevMemoryState     `json:"proposed_summary"`
		} `json:"state"`
		Model     string                  `json:"model"`
		Questions map[string]noulQuestion `json:"questions"`
	}{
		Model: c.model,
		Questions: map[string]noulQuestion{
			"is_faithful": {
				Type:         "noul",
				Instructions: "Is the proposed summary faithful to the source view and free of unsupported claims or contradictions?",
				Criteria: map[string]string{
					"true":  "Every claim is supported by the source view and important durable information is preserved",
					"false": "Adds unsupported information, contradicts the source, or materially misrepresents it",
				},
			},
		},
	}
	payload.State.SourceView = projection
	payload.State.ProposedSummary = summary
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("jev: encode summary validation: %w", err)
	}
	var response anchorResponse
	if err := c.post(ctx, body, &response); err != nil {
		return 0, err
	}
	answer, ok := response.Answers["is_faithful"]
	if !ok {
		return 0, errors.New("jev: response missing answer \"is_faithful\"")
	}
	return answer.Noul, nil
}

// Classify asks Jev to independently score every candidate against the query
// in one request. Best-result selection belongs to the finder engine.
func (c *Client) Classify(ctx context.Context, query string, candidates []entry.JevMemoryState) iter.Seq2[finder.Classification, error] {
	return func(yield func(finder.Classification, error) bool) {
		if c == nil || c.httpClient == nil {
			yield(finder.Classification{}, errors.New("jev: client is not enabled"))
			return
		}
		if strings.TrimSpace(query) == "" {
			yield(finder.Classification{}, errors.New("jev: empty classification query"))
			return
		}
		if len(candidates) == 0 {
			return
		}

		payload := classifyRequest{Model: c.model, Questions: make(map[string]question, len(candidates))}
		payload.State.Query = query
		ids := make([]string, len(candidates))
		for i, candidate := range candidates {
			if candidate.IsZero() {
				yield(finder.Classification{}, fmt.Errorf("jev: candidate %d has empty state", i))
				return
			}
			id := uuid.New().String()
			ids[i] = id
			payload.State.Candidates = append(payload.State.Candidates, candidateState{ID: id, State: candidate})
			payload.Questions[id] = question{
				Type:         "score",
				Instructions: fmt.Sprintf("How relevant is candidate %s to the search query? Judge whether it helps answer or recover the requested earlier context.", id),
				Criteria: []string{
					"Unrelated to the query",
					"Shares a topic but does not help answer the query",
					"Contains relevant information that helps answer the query",
					"Directly answers the query or identifies the requested context",
				},
			}
		}
		body, err := json.Marshal(payload)
		if err != nil {
			yield(finder.Classification{}, fmt.Errorf("jev: encode classification request: %w", err))
			return
		}

		var response classifyResponse
		if err := c.post(ctx, body, &response); err != nil {
			yield(finder.Classification{}, err)
			return
		}
		for i, id := range ids {
			answer, ok := response.Answers[id]
			if !ok {
				yield(finder.Classification{}, fmt.Errorf("jev: response missing answer %q", id))
				return
			}
			if !yield(finder.Classification{Index: i, Score: answer.Score, Confidence: answer.Confidence}, nil) {
				return
			}
		}
	}
}

func (c *Client) post(ctx context.Context, body []byte, target any) error {
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, body)
	if err != nil {
		return fmt.Errorf("jev: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("jev: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		message, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("jev: read error response: %w", err)
		}
		return &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(message))}
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("jev: decode response: %w", err)
	}
	return nil
}

type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("jev: API returned %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	}
	return fmt.Sprintf("jev: API returned %d: %s", e.StatusCode, e.Body)
}

type httpClientTransport struct {
	client HTTPClient
}

func (t httpClientTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.client.Do(req)
}

func checkRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if err != nil {
		return true, nil
	}
	return resp != nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529), nil
}
