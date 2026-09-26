package view

import (
	"errors"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

// ProjectedEntry contains the source data needed to summarize a tape entry.
type ProjectedEntry struct {
	Seq     entry.Seq       `json:"seq"`
	Kind    entry.EntryKind `json:"kind"`
	Summary string          `json:"summary"`
}

// Projection is a bounded, serializable representation of an EntryView.
type Projection struct {
	Scope   EntryRange       `json:"scope"`
	Entries []ProjectedEntry `json:"entries"`
}

// Project excludes derived anchors so a summary is based on source entries.
func (ev EntryView) Project() (Projection, error) {
	projection := Projection{
		Scope:   ev.Scope,
		Entries: make([]ProjectedEntry, 0, len(ev.Raw)),
	}
	for _, e := range ev.Raw {
		if e == nil || e.GetKind().IsAnchor() {
			continue
		}
		projection.Entries = append(projection.Entries, ProjectedEntry{
			Seq: e.GetID(), Kind: e.GetKind(), Summary: e.GetSummary(),
		})
	}
	if len(projection.Entries) == 0 {
		return Projection{}, errors.New("view: empty projection")
	}
	return projection, nil
}
