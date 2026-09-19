package finder

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fixedAnchorDecider float64

func (d fixedAnchorDecider) ShouldAnchor(context.Context, string) (float64, error) {
	return float64(d), nil
}

func TestJevAnchorPolicyCreatesJevAnchor(t *testing.T) {
	t.Parallel()

	policy := NewJevAnchorPolicy(fixedAnchorDecider(.9), .7)
	anchor, ok, err := policy.Anchor(context.Background(), entry.NewEntry(
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent("Remember the production database is in Tokyo."),
		entry.WithEntryOwner("owner-a"),
	), view.EntryRange{SeqS: entry.SeqFromUint64(4), SeqE: entry.SeqFromUint64(5)})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || anchor.GetKind() != entry.EntryKind(entry.AnchorKindJev.String()) {
		t.Fatalf("anchor = %#v, ok = %v", anchor, ok)
	}
	var payload entry.HandoffAnchor
	if err := json.Unmarshal([]byte(anchor.GetSummary()), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Summary != "Remember the production database is in Tokyo." ||
		payload.SeqS != entry.SeqFromUint64(4) ||
		payload.SeqE != entry.SeqFromUint64(5) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestJevAnchorPolicyHonorsThreshold(t *testing.T) {
	t.Parallel()

	_, ok, err := NewJevAnchorPolicy(fixedAnchorDecider(.4), .7).Anchor(
		context.Background(),
		entry.NewEntry(entry.WithEntryContent("transient")),
		view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("entry below threshold was anchored")
	}
}
