package finder

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// JevAnchorDecider returns the probability that a summary should trigger a
// durable Jev memory checkpoint.
type JevAnchorDecider interface {
	ShouldAnchor(context.Context, string) (float64, error)
	ValidateSummary(context.Context, json.RawMessage, entry.JevMemoryState) (float64, error)
}

// JevSummarizer summarizes one assembled tape view into Jev's structured
// memory state. The view retains its range and entries until the provider
// boundary instead of being flattened into an untyped string.
type JevSummarizer interface {
	Summarize(context.Context, view.EntryView) (entry.JevMemoryState, error)
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
	latestSummary := strings.TrimSpace(latest.GetSummary())
	if latestSummary == "" {
		return nil, false, nil
	}
	probability, err := p.Decider.ShouldAnchor(ctx, latestSummary)
	if err != nil {
		return nil, false, err
	}
	if probability < 0 || probability > 1 {
		return nil, false, errors.New("finder: Jev anchor probability must be within [0,1]")
	}
	if probability < p.Threshold {
		return nil, false, nil
	}

	summary, err := p.Summarizer.Summarize(ctx, memory)
	if err != nil {
		return nil, false, err
	}
	if summary.IsZero() {
		return nil, false, errors.New("finder: Jev anchor summarizer returned empty memory state")
	}
	state, err := JevViewState(memory)
	if err != nil {
		return nil, false, err
	}
	faithfulness, err := p.Decider.ValidateSummary(ctx, state, summary)
	if err != nil {
		return nil, false, err
	}
	if faithfulness < 0 || faithfulness > 1 {
		return nil, false, errors.New("finder: Jev summary faithfulness must be within [0,1]")
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

// JevViewState builds the source state seen by both the summarizer and Jev's
// faithfulness check. Anchors are excluded to avoid recursively summarizing
// derived memories.
func JevViewState(memory view.EntryView) (json.RawMessage, error) {
	type summaryEntry struct {
		Seq     entry.Seq       `json:"seq"`
		Kind    entry.EntryKind `json:"kind"`
		Summary string          `json:"summary"`
	}
	state := struct {
		Scope   view.EntryRange `json:"scope"`
		Entries []summaryEntry  `json:"entries"`
	}{Scope: memory.Scope, Entries: make([]summaryEntry, 0, len(memory.Raw))}
	for _, e := range memory.Raw {
		if e == nil || e.GetKind().IsAnchor() {
			continue
		}
		state.Entries = append(state.Entries, summaryEntry{
			Seq: e.GetID(), Kind: e.GetKind(), Summary: e.GetSummary(),
		})
	}
	if len(state.Entries) == 0 {
		return nil, errors.New("finder: empty Jev summary view")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return data, nil
}
