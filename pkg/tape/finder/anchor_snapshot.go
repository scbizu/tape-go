package finder

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// JevAnchorRecord is the indexed state and provenance of a stored Jev anchor.
type JevAnchorRecord struct {
	Seq      entry.Seq
	State    entry.JevMemoryState
	Scope    view.EntryRange
	Replaces []entry.Seq
}

// AnchorSnapshot is the searchable frontier reconstructed from the anchor log.
// Replaced anchors remain in storage but no longer appear in Anchors.
type AnchorSnapshot struct {
	Anchors []JevAnchorRecord
}

// Clone returns an independent snapshot for callers outside the storage lock.
func (s AnchorSnapshot) Clone() AnchorSnapshot {
	out := AnchorSnapshot{Anchors: make([]JevAnchorRecord, len(s.Anchors))}
	for i, anchor := range s.Anchors {
		anchor.Replaces = slices.Clone(anchor.Replaces)
		anchor.State.Facts = slices.Clone(anchor.State.Facts)
		anchor.State.Decisions = slices.Clone(anchor.State.Decisions)
		anchor.State.Constraints = slices.Clone(anchor.State.Constraints)
		anchor.State.Preferences = slices.Clone(anchor.State.Preferences)
		anchor.State.Results = slices.Clone(anchor.State.Results)
		anchor.State.UnresolvedWork = slices.Clone(anchor.State.UnresolvedWork)
		out.Anchors[i] = anchor
	}
	return out
}

// Apply folds one stored anchor into the active snapshot.
func (s *AnchorSnapshot) Apply(anchor JevAnchorRecord) {
	if anchor.State.IsZero() {
		return
	}
	for _, replaced := range anchor.Replaces {
		s.Anchors = slices.DeleteFunc(s.Anchors, func(active JevAnchorRecord) bool {
			return active.Seq == replaced
		})
	}
	s.Anchors = append(s.Anchors, anchor)
}

// AnchorSnapshotReader exposes the active Jev anchors of one owner and session.
type AnchorSnapshotReader interface {
	AnchorSnapshot(context.Context) (AnchorSnapshot, error)
}

// AnchorMetadata is the storage index representation shared by handoff and Jev
// anchors. Summary belongs only to handoff; State belongs only to Jev.
type AnchorMetadata struct {
	JevAnchorRecord
	Kind    entry.AnchorKind
	Summary string
}

// AnchorFromEntry decodes the metadata carried by an anchor.
func AnchorFromEntry(e entry.EntryLike) (AnchorMetadata, bool) {
	if e == nil || !e.GetKind().IsAnchor() {
		return AnchorMetadata{}, false
	}
	var kind entry.AnchorKind
	switch e.GetKind() {
	case entry.EntryKind(entry.AnchorKindHandoff.String()):
		kind = entry.AnchorKindHandoff
	case entry.EntryKind(entry.AnchorKindJev.String()):
		kind = entry.AnchorKindJev
	default:
		return AnchorMetadata{}, false
	}
	var summary string
	var state entry.JevMemoryState
	var replaces []entry.Seq
	var seqS, seqE entry.Seq
	switch kind {
	case entry.AnchorKindHandoff:
		var anchor entry.HandoffAnchor
		if err := json.Unmarshal([]byte(e.GetSummary()), &anchor); err != nil {
			return AnchorMetadata{}, false
		}
		summary, seqS, seqE = anchor.Summary, anchor.SeqS, anchor.SeqE
	case entry.AnchorKindJev:
		var anchor entry.JevAnchor
		if err := json.Unmarshal([]byte(e.GetSummary()), &anchor); err != nil {
			return AnchorMetadata{}, false
		}
		state, seqS, seqE, replaces = anchor.State, anchor.SeqS, anchor.SeqE, anchor.Replaces
	}
	if seqS.Cmp(seqE) > 0 {
		return AnchorMetadata{}, false
	}
	return AnchorMetadata{
		JevAnchorRecord: JevAnchorRecord{
			Seq: e.GetID(), State: state, Replaces: replaces,
			Scope: view.EntryRange{SeqS: seqS, SeqE: seqE},
		},
		Kind: kind, Summary: summary,
	}, true
}

// JevAnchorFromEntry returns only Jev anchors with structured memory state.
func JevAnchorFromEntry(e entry.EntryLike) (JevAnchorRecord, bool) {
	anchor, ok := AnchorFromEntry(e)
	if !ok || anchor.Kind != entry.AnchorKindJev || anchor.State.IsZero() {
		return JevAnchorRecord{}, false
	}
	return anchor.JevAnchorRecord, true
}
