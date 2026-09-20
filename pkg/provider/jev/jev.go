// Package jev adapts TypeSafe AI's Jev API to finder.JevClassifier.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	httpClient HTTPClient
	maxRetries int
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
		client.httpClient = httpClient
		return nil
	}
}

func WithMaxRetries(maxRetries int) Option {
	return func(client *Client) error {
		if maxRetries < 0 {
			return errors.New("jev: max retries cannot be negative")
		}
		client.maxRetries = maxRetries
		return nil
	}
}

func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("jev: empty API key")
	}
	client := &Client{
		apiKey:     apiKey,
		endpoint:   DefaultEndpoint,
		model:      DefaultModel,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		maxRetries: 2,
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
	Type       string  `json:"type"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

type classifyResponse struct {
	Model   string                 `json:"model"`
	Answers map[string]scoreAnswer `json:"answers"`
}

type anchorResponse struct {
	Answers map[string]struct {
		Type string  `json:"type"`
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
	if answer.Type != "noul" {
		return 0, fmt.Errorf("jev: anchor answer has type %q, want noul", answer.Type)
	}
	if answer.Noul < 0 || answer.Noul > 1 {
		return 0, fmt.Errorf("jev: anchor probability %v outside [0,1]", answer.Noul)
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
	if answer.Type != "noul" {
		return 0, fmt.Errorf("jev: summary validation answer has type %q, want noul", answer.Type)
	}
	if answer.Noul < 0 || answer.Noul > 1 {
		return 0, fmt.Errorf("jev: summary faithfulness %v outside [0,1]", answer.Noul)
	}
	return answer.Noul, nil
}

// Classify asks Jev to independently score every candidate against the query
// in one request. Ordering and TopK selection belong to the finder engine.
func (c *Client) Classify(ctx context.Context, query string, candidates []entry.JevMemoryState) ([]finder.Classification, error) {
	if c == nil || c.httpClient == nil {
		return nil, errors.New("jev: client is not enabled")
	}
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("jev: empty classification query")
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	payload := classifyRequest{Model: c.model, Questions: make(map[string]question, len(candidates))}
	payload.State.Query = query
	for i, candidate := range candidates {
		id := candidateID(i)
		if candidate.IsZero() {
			return nil, fmt.Errorf("jev: candidate %d has empty state", i)
		}
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
		return nil, fmt.Errorf("jev: encode classification request: %w", err)
	}

	var response classifyResponse
	if err := c.post(ctx, body, &response); err != nil {
		return nil, err
	}
	results := make([]finder.Classification, 0, len(candidates))
	for i := range candidates {
		id := candidateID(i)
		answer, ok := response.Answers[id]
		if !ok {
			return nil, fmt.Errorf("jev: response missing answer %q", id)
		}
		if answer.Type != "score" {
			return nil, fmt.Errorf("jev: answer %q has type %q, want score", id, answer.Type)
		}
		if answer.Score < 0 || answer.Score > 3 {
			return nil, fmt.Errorf("jev: answer %q has score %v outside [0,3]", id, answer.Score)
		}
		if answer.Confidence < 0 || answer.Confidence > 1 {
			return nil, fmt.Errorf("jev: answer %q has confidence %v outside [0,1]", id, answer.Confidence)
		}
		results = append(results, finder.Classification{Index: i, Score: answer.Score, Confidence: answer.Confidence})
	}
	return results, nil
}

func (c *Client) post(ctx context.Context, body []byte, target any) error {
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("jev: create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt < c.maxRetries && ctx.Err() == nil {
				if err := wait(ctx, retryDelay(nil, attempt)); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("jev: request: %w", err)
		}
		if resp.StatusCode == http.StatusOK {
			err := json.NewDecoder(resp.Body).Decode(target)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("jev: decode response: %w", err)
			}
			return nil
		}
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		resp.Body.Close()
		if retryable(resp.StatusCode) && attempt < c.maxRetries {
			if err := wait(ctx, retryDelay(resp, attempt)); err != nil {
				return err
			}
			continue
		}
		return &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(message))}
	}
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

func candidateID(index int) string { return fmt.Sprintf("candidate_%d", index) }

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == 529
}

func retryDelay(resp *http.Response, attempt int) time.Duration {
	if resp != nil {
		value := resp.Header.Get("Retry-After")
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if at, err := http.ParseTime(value); err == nil {
			return max(time.Until(at), 0)
		}
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
