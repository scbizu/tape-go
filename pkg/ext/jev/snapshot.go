package jev

import (
	"slices"

	generic "github.com/huandu/go-clone/generic"
)

// Snapshot contains the currently effective JEV anchors.
type Snapshot struct {
	Anchors []AnchorRecord
}

func (s Snapshot) Clone() Snapshot { return generic.Clone(s) }

func (s *Snapshot) Apply(anchor AnchorRecord) {
	if anchor.State.IsZero() {
		return
	}
	for _, replaced := range anchor.Replaces {
		s.Anchors = slices.DeleteFunc(s.Anchors, func(active AnchorRecord) bool {
			return active.Seq == replaced
		})
	}
	s.Anchors = append(s.Anchors, anchor)
}
