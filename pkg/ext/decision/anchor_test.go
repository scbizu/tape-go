package decision

import (
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

func TestMemoryStateIsZeroWithoutDecision(t *testing.T) {
	t.Parallel()

	if !(MemoryState{}).IsZero() {
		t.Fatal("empty MemoryState is not zero")
	}
	if !((MemoryState{Decisions: []string{"  "}}).IsZero()) {
		t.Fatal("MemoryState with a blank decision is not zero")
	}
	if (MemoryState{Decisions: []string{"retain durable fact"}}).IsZero() {
		t.Fatal("MemoryState with a decision is zero")
	}
}

func TestAnchorKinds(t *testing.T) {
	t.Parallel()

	anchor, err := NewAnchor(entry.SeqFromUint64(3), "owner-a", Anchor{
		State: MemoryState{Decisions: []string{"durable fact"}},
		SeqS:  entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if anchor.GetKind() != "anchor:decision" {
		t.Fatalf("new anchor kind = %q", anchor.GetKind())
	}
	record, err := AnchorFromEntry(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if record.Seq != anchor.GetID() || record.State.Decisions[0] != "durable fact" {
		t.Fatalf("decoded anchor = %#v", record)
	}
	for _, kind := range []entry.EntryKind{entry.EntryUser, entry.EntryAnchor, "anchor:other"} {
		t.Run(string(kind), func(t *testing.T) {
			candidate := anchor
			candidate.Ek = kind
			if _, err := AnchorFromEntry(candidate); err == nil {
				t.Fatal("accepted an unrelated anchor kind")
			}
		})
	}
}
