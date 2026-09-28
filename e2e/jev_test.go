package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/scbizu/tape-go/pkg/llm"
	jevprovider "github.com/scbizu/tape-go/pkg/provider/jev"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type e2eSummarizer struct{}

func (e2eSummarizer) Summarize(_ context.Context, projection view.Projection) (llm.Summary, error) {
	return llm.Summary{Decisions: []string{projection.Entries[0].Summary}}, nil
}

type e2eJevHTTPClient struct {
	decisions, searches int
}

func (c *e2eJevHTTPClient) Do(r *http.Request) (*http.Response, error) {
	var request struct {
		State     json.RawMessage            `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return nil, err
	}
	answers := make(map[string]any, len(request.Questions))
	if _, ok := request.Questions["should_anchor"]; ok {
		if !bytes.Contains(request.State, []byte(jevFact)) {
			return nil, fmt.Errorf("anchor decision omitted the stored fact: %s", request.State)
		}
		c.decisions++
		answers["should_anchor"] = map[string]any{"choice": "keep"}
	} else {
		if !bytes.Contains(request.State, []byte(jevQuery)) || !bytes.Contains(request.State, []byte(jevFact)) {
			return nil, fmt.Errorf("classification omitted query or candidate: %s", request.State)
		}
		c.searches++
		for id := range request.Questions {
			answers[id] = map[string]any{"score": 1.0, "confidence": 1.0}
		}
	}
	body, err := json.Marshal(map[string]any{"answers": answers})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
}

func TestJevScenario(t *testing.T) {
	stub := &e2eJevHTTPClient{}
	client, err := jevprovider.NewClient("test-key", jevprovider.WithHTTPClient(stub), jevprovider.WithMaxRetries(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx := owner.WithOwnerId(context.Background(), ownerID)
	for _, backend := range e2eBackends() {
		t.Run(backend.name, func(t *testing.T) {
			if err := runJevScenario(ctx, backend, t.TempDir(), e2eSummarizer{}, client); err != nil {
				t.Fatal(err)
			}
		})
	}
	if stub.decisions != len(e2eBackends()) || stub.searches != 2*len(e2eBackends()) {
		t.Fatalf("JEV requests: %d decisions, %d searches", stub.decisions, stub.searches)
	}
}
