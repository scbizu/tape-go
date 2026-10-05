package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"strings"
	"testing"
	"uuid"

	"github.com/scbizu/tape-go/pkg/ext/decision"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failingReadCloser struct {
	err error
}

func (r failingReadCloser) Read([]byte) (int, error) { return 0, r.err }
func (failingReadCloser) Close() error               { return nil }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func collectClassifications(seq iter.Seq2[decision.Classification, error]) ([]decision.Classification, error) {
	var results []decision.Classification
	for result, err := range seq {
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func TestClientClassify(t *testing.T) {
	t.Parallel()

	httpClient := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Fatalf("Authorization = %q", got)
		}
		var request classifyRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != DefaultModel || request.State.Query != "database failure" || len(request.Questions) != 3 {
			t.Fatalf("unexpected request: %#v", request)
		}
		if len(request.State.Candidates[0].State.Decisions) != 1 || request.State.Candidates[0].State.Decisions[0] != "old memory" {
			t.Fatalf("candidate JSON was not sent as structured state: %#v", request.State.Candidates[0].State)
		}
		scores := []float64{1, 3, 2}
		confidence := []float64{.9, .8, .7}
		answers := make(map[string]scoreAnswer, len(request.State.Candidates))
		seen := make(map[string]struct{}, len(request.State.Candidates))
		for i, candidate := range request.State.Candidates {
			if _, err := uuid.Parse(candidate.ID); err != nil {
				t.Fatalf("candidate ID %q is not a UUID: %v", candidate.ID, err)
			}
			if _, ok := seen[candidate.ID]; ok {
				t.Fatalf("duplicate candidate ID %q", candidate.ID)
			}
			seen[candidate.ID] = struct{}{}
			if _, ok := request.Questions[candidate.ID]; !ok {
				t.Fatalf("missing question for candidate ID %q", candidate.ID)
			}
			answers[candidate.ID] = scoreAnswer{Score: scores[i], Confidence: confidence[i]}
		}
		body, err := json.Marshal(classifyResponse{Model: "jev-1.13.0", Answers: answers})
		if err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, string(body)), nil
	})

	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	got, err := collectClassifications(client.Classify(context.Background(), "database failure", []decision.MemoryState{
		{Decisions: []string{"old memory"}}, {Decisions: []string{"best memory"}}, {Decisions: []string{"related memory"}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Index != 0 || got[0].Score != 1 || got[1].Index != 1 || got[1].Score != 3 {
		t.Fatalf("Classify = %#v", got)
	}
}

func TestClientShouldAnchor(t *testing.T) {
	t.Parallel()

	httpClient := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			State     view.Projection           `json:"state"`
			Questions map[string]choiceQuestion `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		question := request.Questions["should_anchor"]
		if len(request.State.Entries) != 2 || request.State.Entries[0].Summary != "earlier fact" || request.State.Entries[1].Summary != "durable fact" ||
			question.Type != "choice" || question.Criteria["keep"] == "" || question.Criteria["skip"] == "" {
			t.Fatalf("unexpected request: %#v", request)
		}
		return jsonResponse(http.StatusOK, `{"answers":{"should_anchor":{"type":"choice","choice":"keep"}}}`), nil
	})
	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	projection := view.Projection{
		Scope: view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(3)},
		Entries: []view.ProjectedEntry{
			{Seq: entry.SeqFromUint64(1), Kind: entry.EntryUser, Summary: "earlier fact"},
			{Seq: entry.SeqFromUint64(2), Kind: entry.EntryAssistant, Summary: "durable fact"},
		},
	}
	got, err := client.ShouldAnchor(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatalf("ShouldAnchor = %v", got)
	}
}

func TestClientShouldAnchorSkips(t *testing.T) {
	t.Parallel()
	httpClient := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"answers":{"should_anchor":{"type":"choice","choice":"skip"}}}`), nil
	})
	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.ShouldAnchor(context.Background(), view.Projection{
		Entries: []view.ProjectedEntry{{Summary: "transient"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("ShouldAnchor accepted a skip decision")
	}
}

func TestClientRetriesRateLimit(t *testing.T) {
	t.Parallel()

	attempts := 0
	httpClient := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			response := jsonResponse(http.StatusTooManyRequests, "")
			response.Header.Set("Retry-After", "0")
			return response, nil
		}
		var request classifyRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		answers := map[string]scoreAnswer{request.State.Candidates[0].ID: {Score: 3, Confidence: 1}}
		body, err := json.Marshal(classifyResponse{Model: "jev-1.13.0", Answers: answers})
		if err != nil {
			t.Fatal(err)
		}
		return jsonResponse(http.StatusOK, string(body)), nil
	})

	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collectClassifications(client.Classify(context.Background(), "query", []decision.MemoryState{{Decisions: []string{"hit"}}})); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestClientReturnsAPIError(t *testing.T) {
	t.Parallel()

	httpClient := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"detail":"invalid key"}`), nil
	})

	client, err := NewClient("bad", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collectClassifications(client.Classify(context.Background(), "query", []decision.MemoryState{{Decisions: []string{"hit"}}}))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Classify error = %v", err)
	}
}

func TestClientReturnsResponseReadError(t *testing.T) {
	t.Parallel()

	want := errors.New("read failed")
	httpClient := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
			Body:       failingReadCloser{err: want},
		}, nil
	})
	client, err := NewClient("bad", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	_, err = collectClassifications(client.Classify(context.Background(), "query", []decision.MemoryState{{Decisions: []string{"hit"}}}))
	if !errors.Is(err, want) {
		t.Fatalf("Classify error = %v, want response read error", err)
	}
}
