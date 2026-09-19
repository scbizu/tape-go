package finder

import (
	"context"
	"encoding/json"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Candidate is a searchable summary and the tape range it represents.
type Candidate struct {
	Seq     entry.Seq
	Kind    entry.AnchorKind
	Summary string
	Scope   view.EntryRange
}

// CandidateIndexer exposes search candidates without prescribing a search
// algorithm such as embeddings or classification.
type CandidateIndexer interface {
	CandidateIndex(context.Context) ([]Candidate, error)
}

// AnchorFromEntry decodes the searchable metadata carried by a handoff anchor.
// Summary may be empty because anchors without summaries still delimit rewind
// ranges, even though they are not useful classifier candidates.
func AnchorFromEntry(e entry.EntryLike) (Candidate, bool) {
	if e == nil || !e.GetKind().IsAnchor() {
		return Candidate{}, false
	}
	var kind entry.AnchorKind
	switch e.GetKind() {
	case entry.EntryKind(entry.AnchorKindHandoff.String()):
		kind = entry.AnchorKindHandoff
	case entry.EntryKind(entry.AnchorKindJev.String()):
		kind = entry.AnchorKindJev
	default:
		return Candidate{}, false
	}
	summary := e.GetSummary()
	var anchor entry.HandoffAnchor
	if err := json.Unmarshal([]byte(summary), &anchor); err != nil || anchor.SeqS.Cmp(anchor.SeqE) > 0 {
		return Candidate{}, false
	}
	return Candidate{
		Seq:     e.GetID(),
		Kind:    kind,
		Summary: anchor.Summary,
		Scope:   view.EntryRange{SeqS: anchor.SeqS, SeqE: anchor.SeqE},
	}, true
}

// CandidateFromAnchor returns only Jev anchors with a non-empty memory summary.
// Ordinary entries and handoff anchors are not Jev search candidates.
func CandidateFromAnchor(e entry.EntryLike) (Candidate, bool) {
	candidate, ok := AnchorFromEntry(e)
	return candidate, ok && candidate.Kind == entry.AnchorKindJev && candidate.Summary != ""
}
