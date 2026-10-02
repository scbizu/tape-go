package ds

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	deepseek "github.com/cohesion-org/deepseek-go"
	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type modelCardHTTPFunc func(*http.Request) (*http.Response, error)

func (f modelCardHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestStructuredRequestsUseDiscoveredModelLimits(t *testing.T) {
	for _, tc := range []struct {
		name, cards string
		status      int
		wantLimit   string
		wantError   bool
	}{
		{"card limit", `{"data":[{"id":"custom-model","context_window":32000,"max_output_tokens":12000}]}`, 200, `"max_tokens":12000`, false},
		{"different model limit", `{"data":[{"id":"custom-model","max_output_tokens":60000}]}`, 200, `"max_tokens":60000`, false},
		{"context is not output limit", `{"data":[{"id":"custom-model","context_window":32000}]}`, 200, "", false},
		{"unknown alias", `{"data":[{"id":"other-model","max_output_tokens":60000}]}`, 200, "", false},
		{"unsupported discovery", `{}`, 404, "", false},
		{"unauthorized", `{"error":{"message":"unauthorized","type":"authentication_error"}}`, 401, "", true},
		{"malformed metadata", `{`, 200, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gets, posts := 0, 0
			httpClient := modelCardHTTPFunc(func(r *http.Request) (*http.Response, error) {
				status, body := 200, `{"id":"completion-test","model":"custom-model","object":"chat.completion","created":1,"choices":[{"message":{"content":"{\"decisions\":[\"fact\"],\"order\":[0]}"},"finish_reason":"stop"}]}`
				if r.Method == http.MethodGet {
					gets++
					if r.URL.String() != "https://models.test/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
						t.Errorf("unexpected model discovery URL or authentication")
					}
					status, body = tc.status, tc.cards
				} else {
					posts++
					payload, err := io.ReadAll(r.Body)
					if err != nil {
						return nil, err
					}
					if tc.wantLimit == "" {
						if strings.Contains(string(payload), `"max_tokens"`) {
							t.Error("missing model metadata must use server defaults")
						}
					} else if !strings.Contains(string(payload), tc.wantLimit) {
						t.Error("request did not use model-card output limit")
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			m, err := NewModel("test-key", "custom-model", deepseek.WithBaseURL("https://models.test/v1/"), deepseek.WithHTTPClient(httpClient))
			if err != nil {
				t.Fatal(err)
			}
			var model llm.Model = m
			limit, err := model.MaxTokenLimit(context.Background())
			if tc.wantError {
				if err == nil || posts != 0 {
					t.Fatal("expected model limit discovery failure without a completion")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantLimit == "" && limit != 0 {
				t.Fatalf("unknown output limit = %d, want 0", limit)
			}
			if tc.wantLimit != "" && fmt.Sprintf(`"max_tokens":%d`, limit) != tc.wantLimit {
				t.Fatalf("discovered output limit = %d, want %s", limit, tc.wantLimit)
			}
			_, err = m.Summarize(context.Background(), view.Projection{Entries: []view.ProjectedEntry{{Summary: "fact"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.ReRank(context.Background(), "fact", []string{"fact"}); err != nil {
				t.Fatal(err)
			}
			if gets != 1 || posts != 2 {
				t.Fatalf("metadata cache: %d GETs, %d POSTs", gets, posts)
			}
		})
	}
}

func TestMaxTokenLimitUnavailableModelAndCanceledContext(t *testing.T) {
	for _, m := range []*Model{nil, {}, {client: &fakeClient{}}} {
		if _, err := m.MaxTokenLimit(context.Background()); err == nil {
			t.Fatal("disabled model must return an error")
		}
	}
	m := &Model{client: &fakeClient{}, name: "custom-model"}
	if limit, err := m.MaxTokenLimit(context.Background()); err != nil || limit != 0 {
		t.Fatalf("client without metadata: limit %d, error %v", limit, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.MaxTokenLimit(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
}

func TestModelCardDiscoveryRetriesTransientFailure(t *testing.T) {
	calls := 0
	client, err := deepseek.NewClientWithOptions("test-key", deepseek.WithHTTPClient(modelCardHTTPFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		status, body := 200, `{"data":[{"id":"model","max_output_tokens":8000}]}`
		if calls == 1 {
			status, body = 500, `{"error":{"message":"temporary","type":"server_error"}}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	cards := &modelCardClient{Client: client}
	if _, err := cards.OutputLimit(context.Background(), "model"); err == nil {
		t.Fatal("expected discovery failure")
	}
	if limit, err := cards.OutputLimit(context.Background(), "model"); err != nil || limit != 8000 {
		t.Fatalf("retry limit %d, error %v", limit, err)
	}
}
