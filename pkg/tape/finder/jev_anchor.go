package finder

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// JevAnchorDecider returns the probability that a summary should trigger a
// durable Jev memory checkpoint.
type JevAnchorDecider interface {
	ShouldAnchor(context.Context, string) (float64, error)
	ValidateSummary(context.Context, string, string) (float64, error)
}

// JevAnchorPolicy lets Jev decide when to checkpoint, then delegates the
// bounded active-view summary to an LLM provider.
type JevAnchorPolicy struct {
	Decider    JevAnchorDecider
	Summarizer llm.Summarizer
	Threshold  float64
}

func NewJevAnchorPolicy(decider JevAnchorDecider, summarizer llm.Summarizer, threshold float64) JevAnchorPolicy {
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

	state, err := summaryState(memory)
	if err != nil {
		return nil, false, err
	}
	summary, err := p.Summarizer.Summarize(ctx, state)
	if err != nil {
		return nil, false, err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return nil, false, errors.New("finder: Jev anchor summarizer returned empty summary")
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
		Summary: summary,
		SeqS:    memory.Scope.SeqS,
		SeqE:    memory.Scope.SeqE,
	})
	if err != nil {
		return nil, false, err
	}
	return anchor, true, nil
}

func summaryState(memory view.EntryView) (string, error) {
	type summaryEntry struct {
		Seq     entry.Seq       `json:"seq"`
		Kind    entry.EntryKind `json:"kind"`
		Summary string          `json:"summary"`
	}
	state := struct {
		Entries []summaryEntry `json:"entries"`
	}{Entries: make([]summaryEntry, 0, len(memory.Raw))}
	for _, e := range memory.Raw {
		if e == nil || e.GetKind().IsAnchor() {
			continue
		}
		state.Entries = append(state.Entries, summaryEntry{
			Seq: e.GetID(), Kind: e.GetKind(), Summary: e.GetSummary(),
		})
	}
	if len(state.Entries) == 0 {
		return "", errors.New("finder: empty Jev summary view")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
