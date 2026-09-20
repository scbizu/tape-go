// Package view is the view assembler of tape
package view

import (
	"context"
	"encoding/json"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

type EntryRange struct {
	SeqS entry.Seq
	SeqE entry.Seq
}

// EntryView describes a scoped entry view assembler
type EntryView struct {
	SessionId string
	Owner     string
	// Scope is the entry range index
	// Technically, [SeqS,SeqE)
	Scope EntryRange

	Raw []entry.EntryLike
	// Optional . If we need to integrate with some semantic search
	Summary string

	// AnchorMaker is an optional post-store handler bound to this view. It is
	// deliberately excluded from the serialized view representation.
	AnchorMaker AnchorMaker
}

func (ev EntryView) MarshalJSON() ([]byte, error) {
	return json.Marshal(ev.Raw)
}

func (ev EntryView) MakeAnchor(ctx context.Context, latest entry.EntryLike) (entry.EntryLike, bool, error) {
	if ev.AnchorMaker == nil {
		return nil, false, nil
	}
	return ev.AnchorMaker.MakeAnchor(ctx, latest, ev)
}

// TapeView is a special view assemble without entry raw data.
// It describes the metadata info about the tape itself
type TapeView struct {
	SessionID string
	Owner     string
	Scope     EntryRange
}
