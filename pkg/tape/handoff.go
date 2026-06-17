package tape

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type HandOffOption func(*entry.HandoffAnchor)

func WithHandoffSummary(summary string) HandOffOption {
	return func(anchor *entry.HandoffAnchor) {
		anchor.Summary = summary
	}
}

func WithHandoffSeqRange(r view.EntryRange) HandOffOption {
	return func(anchor *entry.HandoffAnchor) {
		anchor.SeqS = r.SeqS
		anchor.SeqE = r.SeqE
	}
}

func (t *Tape) HandOff(
	ctx context.Context,
	opts ...HandOffOption,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := owner.GetOwnerId(ctx); err != nil {
		ctx = owner.WithOwnerId(ctx, t.OwnerID)
	}

	tv, err := t.Get(ctx)
	if err != nil {
		return fmt.Errorf("tape: %w", err)
	}
	if tv.Scope.SeqE == 0 {
		return fmt.Errorf("tape: handoff empty tape")
	}

	anchorSeq := entry.NextEntryID(tv.Scope.SeqE)
	anchor := entry.HandoffAnchor{
		// range: [SeqS,SeqE)
		SeqS: t.View.SeqS,
		SeqE: anchorSeq,
	}
	if anchor.SeqS == 0 {
		anchor.SeqS = 1
	}
	for _, opt := range opts {
		opt(&anchor)
	}
	if anchor.SeqS > anchor.SeqE {
		return fmt.Errorf(
			"tape: invalid handoff range [%d,%d)",
			anchor.SeqS,
			anchor.SeqE,
		)
	}

	payload, err := json.Marshal(anchor)
	if err != nil {
		return fmt.Errorf("tape: marshal handoff anchor: %w", err)
	}
	if err := t.Store(
		ctx,
		entry.NewAnchor(anchorSeq, tv.Owner, entry.AnchorKindHandoff, payload),
	); err != nil {
		return fmt.Errorf("tape: %w", err)
	}

	t.View = view.EntryRange{
		SeqS: entry.NextEntryID(anchorSeq),
	}
	t.resetReadState()
	return nil
}
