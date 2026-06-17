package tape

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// HandoffAnchor describes the payload carried by an anchor:handoff entry.
//
// Tape exposes an entryView as the sliding window that an agent can currently
// work with. When that window grows too large, the agent or an upper layer can
// summarize the current entryView, write a handoff anchor for the covered range,
// archive the older entries, and then reset the sliding window after the anchor.
//
// Summary is the compact memory of the archived window. SeqRange records the
// original entries covered by that summary so a future agent can look back into
// the archive when the summary is not enough.
type HandoffAnchor struct {
	Summary  string
	SeqRange view.EntryRange
}

type HandOffOption func(*HandoffAnchor)

func WithHandoffSummary(summary string) HandOffOption {
	return func(anchor *HandoffAnchor) {
		anchor.Summary = summary
	}
}

func WithHandoffSeqRange(r view.EntryRange) HandOffOption {
	return func(anchor *HandoffAnchor) {
		anchor.SeqRange = r
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
	anchor := HandoffAnchor{
		// range: [SeqS,SeqE)
		SeqRange: view.EntryRange{
			SeqS: t.View.SeqS,
			SeqE: anchorSeq,
		},
	}
	if anchor.SeqRange.SeqS == 0 {
		anchor.SeqRange.SeqS = 1
	}
	for _, opt := range opts {
		opt(&anchor)
	}
	if anchor.SeqRange.SeqS > anchor.SeqRange.SeqE {
		return fmt.Errorf(
			"tape: invalid handoff range [%d,%d)",
			anchor.SeqRange.SeqS,
			anchor.SeqRange.SeqE,
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
