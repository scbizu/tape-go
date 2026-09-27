package jev

import (
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

func TestSnapshotCloneIsIndependent(t *testing.T) {
	original := Snapshot{Anchors: []AnchorRecord{{
		Seq:      entry.SeqFromUint64(3),
		State:    MemoryState{Decisions: []string{"original"}},
		Replaces: []entry.Seq{entry.SeqFromUint64(1)},
	}}}

	cloned := original.Clone()
	cloned.Anchors[0].State.Decisions[0] = "changed"
	cloned.Anchors[0].Replaces[0] = entry.SeqFromUint64(2)

	if original.Anchors[0].State.Decisions[0] != "original" || original.Anchors[0].Replaces[0] != entry.SeqFromUint64(1) {
		t.Fatalf("clone mutated original: %#v", original.Anchors[0])
	}
	if cloned.Anchors[0].Seq != entry.SeqFromUint64(3) {
		t.Fatalf("clone lost anchor ID: %s", cloned.Anchors[0].Seq)
	}
}
