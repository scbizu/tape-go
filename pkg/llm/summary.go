package llm

import (
	"context"
	"strings"

	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Summary holds durable statements extracted from a view projection.
type Summary struct {
	Decisions []string `json:"decisions"`
}

func (s Summary) IsZero() bool {
	for _, decision := range s.Decisions {
		if strings.TrimSpace(decision) != "" {
			return false
		}
	}
	return true
}

// Summarizer is an optional capability of an LLM provider.
type Summarizer interface {
	Summarize(context.Context, view.Projection) (Summary, error)
}
