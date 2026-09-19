package finder

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// JevAnchorDecider returns the probability that a summary should become a
// durable Jev memory point.
type JevAnchorDecider interface {
	ShouldAnchor(context.Context, string) (float64, error)
}

// JevAnchorPolicy turns entries selected by Jev into anchor:jev entries.
type JevAnchorPolicy struct {
	Decider   JevAnchorDecider
	Threshold float64
}

func NewJevAnchorPolicy(decider JevAnchorDecider, threshold float64) JevAnchorPolicy {
	return JevAnchorPolicy{Decider: decider, Threshold: threshold}
}

func (p JevAnchorPolicy) Anchor(ctx context.Context, e entry.EntryLike, scope view.EntryRange) (entry.EntryLike, bool, error) {
	if p.Decider == nil {
		return nil, false, errors.New("finder: nil Jev anchor decider")
	}
	if p.Threshold < 0 || p.Threshold > 1 {
		return nil, false, errors.New("finder: Jev anchor threshold must be within [0,1]")
	}
	if e == nil || e.GetKind().IsAnchor() {
		return nil, false, nil
	}
	summary := strings.TrimSpace(e.GetSummary())
	if summary == "" {
		return nil, false, nil
	}
	probability, err := p.Decider.ShouldAnchor(ctx, summary)
	if err != nil {
		return nil, false, err
	}
	if probability < 0 || probability > 1 {
		return nil, false, errors.New("finder: Jev anchor probability must be within [0,1]")
	}
	if probability < p.Threshold {
		return nil, false, nil
	}
	payload, err := json.Marshal(entry.HandoffAnchor{Summary: summary, SeqS: scope.SeqS, SeqE: scope.SeqE})
	if err != nil {
		return nil, false, err
	}
	anchor := entry.NewAnchor(entry.Seq{}, e.GetOwner(), entry.AnchorKindJev, payload)
	return anchor, true, nil
}
