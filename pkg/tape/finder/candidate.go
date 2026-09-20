package finder

import (
	"context"
	"encoding/json"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Candidate is a structured Jev state and the tape range it represents.
type Candidate struct {
	Seq   entry.Seq
	State entry.JevMemoryState
	Scope view.EntryRange
}

// CandidateIndexer exposes search candidates without prescribing a search
// algorithm such as embeddings or classification.
type CandidateIndexer interface {
	CandidateIndex(context.Context) ([]Candidate, error)
}

// AnchorMetadata is the storage index representation shared by handoff and Jev
// anchors. Summary belongs only to handoff; State belongs only to Jev.
type AnchorMetadata struct {
	Seq     entry.Seq
	Kind    entry.AnchorKind
	Summary string
	State   entry.JevMemoryState
	Scope   view.EntryRange
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
		state, seqS, seqE = anchor.State, anchor.SeqS, anchor.SeqE
	}
	if seqS.Cmp(seqE) > 0 {
		return AnchorMetadata{}, false
	}
	return AnchorMetadata{
		Seq:     e.GetID(),
		Kind:    kind,
		Summary: summary,
		State:   state,
		Scope:   view.EntryRange{SeqS: seqS, SeqE: seqE},
	}, true
}

// CandidateFromAnchor returns only Jev anchors with structured memory state.
// Ordinary entries and handoff anchors are not Jev search candidates.
func CandidateFromAnchor(e entry.EntryLike) (Candidate, bool) {
	anchor, ok := AnchorFromEntry(e)
	if !ok || anchor.Kind != entry.AnchorKindJev || anchor.State.IsZero() {
		return Candidate{}, false
	}
	return Candidate{Seq: anchor.Seq, State: anchor.State, Scope: anchor.Scope}, true
}
