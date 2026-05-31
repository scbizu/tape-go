package skills

import (
	"context"

	"github.com/scbizu/tape-go/internal/tape"
)

type HandoffResult struct {
	HandoffWritten bool
	AnchorWritten  bool
	Summary        string
}

type HandoffWriter interface {
	Handoff(ctx context.Context, sessionID string, input tape.HandoffInput) (*tape.Anchor, error)
}

type HandoffBridge struct {
	writer HandoffWriter
}

func NewHandoffBridge(writer HandoffWriter) *HandoffBridge {
	return &HandoffBridge{writer: writer}
}

func (b *HandoffBridge) Apply(ctx context.Context, sessionID string, input tape.HandoffInput) (*HandoffResult, error) {
	anchor, err := b.writer.Handoff(ctx, sessionID, input)
	if err != nil {
		return nil, err
	}
	return &HandoffResult{
		HandoffWritten: true,
		AnchorWritten:  anchor != nil,
		Summary:        input.Summary,
	}, nil
}
