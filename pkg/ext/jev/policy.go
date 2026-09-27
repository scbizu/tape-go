package jev

import (
	"context"
	"errors"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// AnchorDecider decides whether a complete view projection should trigger a
// durable Jev memory checkpoint.
type AnchorDecider interface {
	ShouldAnchor(context.Context, view.Projection) (bool, error)
}

// AnchorPolicy lets Jev decide when to checkpoint, then delegates the
// bounded active-view summary to an LLM provider.
type AnchorPolicy struct {
	Decider    AnchorDecider  `validate:"required"`
	Summarizer llm.Summarizer `validate:"required"`
}

func NewAnchorPolicy(decider AnchorDecider, summarizer llm.Summarizer) AnchorPolicy {
	return AnchorPolicy{Decider: decider, Summarizer: summarizer}
}

func (p AnchorPolicy) MakeAnchor(ctx context.Context, latest entry.EntryLike, memory view.EntryView) (entry.EntryLike, bool, error) {
	if err := validateStructure("Jev anchor policy", p); err != nil {
		return nil, false, err
	}
	if latest == nil || latest.GetKind().IsAnchor() {
		return nil, false, nil
	}
	projection, err := memory.Project()
	if err != nil {
		return nil, false, err
	}
	shouldAnchor, err := p.Decider.ShouldAnchor(ctx, projection)
	if err != nil {
		return nil, false, err
	}
	if !shouldAnchor {
		return nil, false, nil
	}

	summary, err := p.Summarizer.Summarize(ctx, projection)
	if err != nil {
		return nil, false, err
	}
	if summary.IsZero() {
		return nil, false, errors.New("jev: anchor summarizer returned empty memory state")
	}
	ownerID := latest.GetOwner()
	if ownerID == "" {
		ownerID = memory.Owner
	}
	anchor, err := NewAnchor(entry.Seq{}, ownerID, Anchor{
		State: summary,
		SeqS:  memory.Scope.SeqS,
		SeqE:  memory.Scope.SeqE,
	})
	if err != nil {
		return nil, false, err
	}
	return anchor, true, nil
}
