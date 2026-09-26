package jev

import (
	"context"
	"errors"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// ViewEntry is the non-derived entry representation exposed to Jev.
type ViewEntry struct {
	Seq     entry.Seq       `json:"seq"`
	Kind    entry.EntryKind `json:"kind"`
	Summary string          `json:"summary"`
}

// ViewProjection is the stable projection shared by anchor decision,
// summarization, and faithfulness validation.
type ViewProjection struct {
	Scope   view.EntryRange `json:"scope"`
	Entries []ViewEntry     `json:"entries"`
}

// AnchorDecider returns the probability that a complete view projection
// should trigger a durable Jev memory checkpoint.
type AnchorDecider interface {
	ShouldAnchor(context.Context, ViewProjection) (float64, error)
	ValidateSummary(context.Context, ViewProjection, MemoryState) (float64, error)
}

// Summarizer summarizes one assembled tape view into Jev's structured
// memory state. The view retains its range and entries until the provider
// boundary instead of being flattened into an untyped string.
type Summarizer interface {
	Summarize(context.Context, ViewProjection) (MemoryState, error)
}

// AnchorPolicy lets Jev decide when to checkpoint, then delegates the
// bounded active-view summary to an LLM provider.
type AnchorPolicy struct {
	Decider    AnchorDecider `validate:"required"`
	Summarizer Summarizer    `validate:"required"`
	Threshold  float64       `validate:"gte=0,lte=1"`
}

func NewAnchorPolicy(decider AnchorDecider, summarizer Summarizer, threshold float64) AnchorPolicy {
	return AnchorPolicy{Decider: decider, Summarizer: summarizer, Threshold: threshold}
}

func (p AnchorPolicy) MakeAnchor(ctx context.Context, latest entry.EntryLike, memory view.EntryView) (entry.EntryLike, bool, error) {
	if err := validateStructure("Jev anchor policy", p); err != nil {
		return nil, false, err
	}
	if latest == nil || latest.GetKind().IsAnchor() {
		return nil, false, nil
	}
	projection, err := ProjectJevView(memory)
	if err != nil {
		return nil, false, err
	}
	probability, err := p.Decider.ShouldAnchor(ctx, projection)
	if err != nil {
		return nil, false, err
	}
	if probability < p.Threshold {
		return nil, false, nil
	}

	summary, err := p.Summarizer.Summarize(ctx, projection)
	if err != nil {
		return nil, false, err
	}
	if summary.IsZero() {
		return nil, false, errors.New("jev: anchor summarizer returned empty memory state")
	}
	faithfulness, err := p.Decider.ValidateSummary(ctx, projection, summary)
	if err != nil {
		return nil, false, err
	}
	if faithfulness < p.Threshold {
		return nil, false, nil
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

// ProjectJevView builds the source state seen by both the summarizer and Jev's
// faithfulness check. Anchors are excluded to avoid recursively summarizing
// derived memories.
func ProjectJevView(memory view.EntryView) (ViewProjection, error) {
	projection := ViewProjection{
		Scope:   memory.Scope,
		Entries: make([]ViewEntry, 0, len(memory.Raw)),
	}
	for _, e := range memory.Raw {
		if e == nil || e.GetKind().IsAnchor() {
			continue
		}
		projection.Entries = append(projection.Entries, ViewEntry{
			Seq: e.GetID(), Kind: e.GetKind(), Summary: e.GetSummary(),
		})
	}
	if len(projection.Entries) == 0 {
		return ViewProjection{}, errors.New("jev: empty view projection")
	}
	return projection, nil
}
