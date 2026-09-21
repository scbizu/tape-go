package finder

import (
	"context"
	"errors"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// JevViewEntry is the non-derived entry representation exposed to Jev.
type JevViewEntry struct {
	Seq     entry.Seq       `json:"seq"`
	Kind    entry.EntryKind `json:"kind"`
	Summary string          `json:"summary"`
}

// JevViewProjection is the stable projection shared by anchor decision,
// summarization, and faithfulness validation.
type JevViewProjection struct {
	Scope   view.EntryRange `json:"scope"`
	Entries []JevViewEntry  `json:"entries"`
}

// JevAnchorDecider returns the probability that a complete view projection
// should trigger a durable Jev memory checkpoint.
type JevAnchorDecider interface {
	ShouldAnchor(context.Context, JevViewProjection) (float64, error)
	ValidateSummary(context.Context, JevViewProjection, entry.JevMemoryState) (float64, error)
}

// JevSummarizer summarizes one assembled tape view into Jev's structured
// memory state. The view retains its range and entries until the provider
// boundary instead of being flattened into an untyped string.
type JevSummarizer interface {
	Summarize(context.Context, JevViewProjection) (entry.JevMemoryState, error)
}

// JevAnchorPolicy lets Jev decide when to checkpoint, then delegates the
// bounded active-view summary to an LLM provider.
type JevAnchorPolicy struct {
	Decider    JevAnchorDecider
	Summarizer JevSummarizer
	Threshold  float64
}

func NewJevAnchorPolicy(decider JevAnchorDecider, summarizer JevSummarizer, threshold float64) JevAnchorPolicy {
	return JevAnchorPolicy{Decider: decider, Summarizer: summarizer, Threshold: threshold}
}

func (p JevAnchorPolicy) MakeAnchor(ctx context.Context, latest entry.EntryLike, memory view.EntryView) (entry.EntryLike, bool, error) {
	if p.Decider == nil {
		return nil, false, errors.New("finder: nil Jev anchor decider")
	}
	if p.Summarizer == nil {
		return nil, false, errors.New("finder: nil Jev anchor summarizer")
	}
	if p.Threshold < 0 || p.Threshold > 1 {
		return nil, false, errors.New("finder: Jev anchor threshold must be within [0,1]")
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
		return nil, false, errors.New("finder: Jev anchor summarizer returned empty memory state")
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
	anchor, err := entry.NewJevAnchor(entry.Seq{}, ownerID, entry.JevAnchor{
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
func ProjectJevView(memory view.EntryView) (JevViewProjection, error) {
	projection := JevViewProjection{
		Scope:   memory.Scope,
		Entries: make([]JevViewEntry, 0, len(memory.Raw)),
	}
	for _, e := range memory.Raw {
		if e == nil || e.GetKind().IsAnchor() {
			continue
		}
		projection.Entries = append(projection.Entries, JevViewEntry{
			Seq: e.GetID(), Kind: e.GetKind(), Summary: e.GetSummary(),
		})
	}
	if len(projection.Entries) == 0 {
		return JevViewProjection{}, errors.New("finder: empty Jev view projection")
	}
	return projection, nil
}
