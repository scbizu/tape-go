// Package jev adds durable JEV anchoring and retrieval to a tape.
package jev

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const Kind entry.EntryKind = "anchor:jev"

// MemoryState is the structured memory retained by a JEV anchor.
type MemoryState = llm.Summary

// Anchor is the persisted JEV payload. Its JSON shape matches existing tapes.
type Anchor struct {
	State      MemoryState
	SeqS, SeqE entry.Seq
	Replaces   []entry.Seq
}

func NewAnchor(seq entry.Seq, owner string, anchor Anchor) (entry.Entry, error) {
	if anchor.State.IsZero() {
		return entry.Entry{}, errors.New("jev: anchor state is empty")
	}
	payload, err := json.Marshal(anchor)
	if err != nil {
		return entry.Entry{}, fmt.Errorf("jev: encode anchor: %w", err)
	}
	return entry.NewEntry(
		entry.WithEntryID(seq),
		entry.WithEntryOwner(owner),
		entry.WithEntryKind(Kind),
		entry.WithEntryContent(string(payload)),
	), nil
}

type AnchorRecord struct {
	Seq      entry.Seq
	State    MemoryState
	Scope    view.EntryRange
	Replaces []entry.Seq
}

func AnchorFromEntry(e entry.EntryLike) (AnchorRecord, error) {
	if e == nil || e.GetKind() != Kind {
		return AnchorRecord{}, errors.New("jev: entry is not a JEV anchor")
	}
	var anchor Anchor
	if err := json.Unmarshal([]byte(e.GetSummary()), &anchor); err != nil {
		return AnchorRecord{}, fmt.Errorf("jev: decode anchor %s: %w", e.GetID(), err)
	}
	if anchor.State.IsZero() || anchor.SeqS.Cmp(anchor.SeqE) > 0 {
		return AnchorRecord{}, fmt.Errorf("jev: invalid anchor %s", e.GetID())
	}
	return AnchorRecord{
		Seq: e.GetID(), State: anchor.State,
		Scope:    view.EntryRange{SeqS: anchor.SeqS, SeqE: anchor.SeqE},
		Replaces: slices.Clone(anchor.Replaces),
	}, nil
}
