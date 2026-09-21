package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/finder"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) Do(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
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
		return jsonResponse(http.StatusOK, `{
			"model":"jev-1.13.0",
			"answers":{
				"candidate_0":{"type":"score","score":1.0,"confidence":0.9},
				"candidate_1":{"type":"score","score":3.0,"confidence":0.8},
				"candidate_2":{"type":"score","score":2.0,"confidence":0.7}
			}
		}`), nil
	})

	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Classify(context.Background(), "database failure", []entry.JevMemoryState{
		{Decisions: []string{"old memory"}}, {Decisions: []string{"best memory"}}, {Decisions: []string{"related memory"}},
	})
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
			State     finder.JevViewProjection `json:"state"`
			Questions map[string]noulQuestion  `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		question := request.Questions["should_anchor"]
		if len(request.State.Entries) != 2 || request.State.Entries[0].Summary != "earlier fact" || request.State.Entries[1].Summary != "durable fact" ||
			question.Type != "noul" || question.Criteria["true"] == "" {
			t.Fatalf("unexpected request: %#v", request)
		}
		return jsonResponse(http.StatusOK, `{"answers":{"should_anchor":{"type":"noul","noul":0.91}}}`), nil
	})
	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	projection := finder.JevViewProjection{
		Scope: view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(3)},
		Entries: []finder.JevViewEntry{
			{Seq: entry.SeqFromUint64(1), Kind: entry.EntryUser, Summary: "earlier fact"},
			{Seq: entry.SeqFromUint64(2), Kind: entry.EntryAssistant, Summary: "durable fact"},
		},
	}
	got, err := client.ShouldAnchor(context.Background(), projection)
	if err != nil {
		t.Fatal(err)
	}
	if got != .91 {
		t.Fatalf("ShouldAnchor = %v", got)
	}
}

func TestClientValidateSummary(t *testing.T) {
	t.Parallel()

	httpClient := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request struct {
			State struct {
				SourceView      finder.JevViewProjection `json:"source_view"`
				ProposedSummary entry.JevMemoryState     `json:"proposed_summary"`
			} `json:"state"`
			Questions map[string]noulQuestion `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.State.SourceView.Entries) != 1 || len(request.State.ProposedSummary.Decisions) != 1 ||
			request.State.ProposedSummary.Decisions[0] != "summary" || request.Questions["is_faithful"].Type != "noul" {
			t.Fatalf("unexpected request: %#v", request)
		}
		return jsonResponse(http.StatusOK, `{"answers":{"is_faithful":{"type":"noul","noul":0.96}}}`), nil
	})
	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	projection := finder.JevViewProjection{
		Scope:   view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)},
		Entries: []finder.JevViewEntry{{Seq: entry.SeqFromUint64(1), Kind: entry.EntryUser, Summary: "source"}},
	}
	got, err := client.ValidateSummary(context.Background(), projection, entry.JevMemoryState{Decisions: []string{"summary"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != .96 {
		t.Fatalf("ValidateSummary = %v", got)
	}
}

func TestClientRetriesRateLimit(t *testing.T) {
	t.Parallel()

	attempts := 0
	httpClient := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			response := jsonResponse(http.StatusTooManyRequests, "")
			response.Header.Set("Retry-After", "0")
			return response, nil
		}
		return jsonResponse(http.StatusOK, `{"model":"jev-1.13.0","answers":{"candidate_0":{"type":"score","score":3,"confidence":1}}}`), nil
	})

	client, err := NewClient("secret", WithHTTPClient(httpClient), WithMaxRetries(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Classify(context.Background(), "query", []entry.JevMemoryState{{Decisions: []string{"hit"}}}); err != nil {
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
	_, err = client.Classify(context.Background(), "query", []entry.JevMemoryState{{Decisions: []string{"hit"}}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Classify error = %v", err)
	}
}
