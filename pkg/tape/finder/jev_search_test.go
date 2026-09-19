package finder

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fakeJevClassifier struct {
	candidates []string
	results    []Classification
}

func TestAnchorKindsHaveSeparateSearchSemantics(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(entry.HandoffAnchor{
		Summary: "archived decision",
		SeqS:    entry.SeqFromUint64(4),
		SeqE:    entry.SeqFromUint64(9),
	})
	if err != nil {
		t.Fatal(err)
	}
	handoff := entry.NewAnchor(entry.SeqFromUint64(10), "owner-a", entry.AnchorKindHandoff, payload)
	candidate, ok := AnchorFromEntry(handoff)
	if !ok {
		t.Fatal("AnchorFromEntry rejected handoff anchor")
	}
	if candidate.Seq != entry.SeqFromUint64(10) ||
		candidate.Summary != "archived decision" ||
		candidate.Scope != (view.EntryRange{SeqS: entry.SeqFromUint64(4), SeqE: entry.SeqFromUint64(9)}) {
		t.Fatalf("candidate = %#v", candidate)
	}
	if _, ok := CandidateFromAnchor(handoff); ok {
		t.Fatal("handoff anchor became a Jev candidate")
	}
	jevAnchor := entry.NewAnchor(entry.SeqFromUint64(10), "owner-a", entry.AnchorKindJev, payload)
	if _, ok := CandidateFromAnchor(jevAnchor); !ok {
		t.Fatal("Jev anchor was not a Jev candidate")
	}
	if _, ok := CandidateFromAnchor(entry.NewEntry(entry.WithEntryContent("ordinary"))); ok {
		t.Fatal("ordinary entry became a Jev candidate")
	}
	emptyPayload, err := json.Marshal(entry.HandoffAnchor{
		SeqS: entry.SeqFromUint64(4),
		SeqE: entry.SeqFromUint64(9),
	})
	if err != nil {
		t.Fatal(err)
	}
	emptyAnchor := entry.NewAnchor(entry.SeqFromUint64(11), "owner-a", entry.AnchorKindHandoff, emptyPayload)
	if _, ok := AnchorFromEntry(emptyAnchor); !ok {
		t.Fatal("empty-summary rewind anchor was not indexed")
	}
	if _, ok := CandidateFromAnchor(emptyAnchor); ok {
		t.Fatal("empty-summary anchor became a Jev candidate")
	}
}

func (c *fakeJevClassifier) Classify(_ context.Context, _ string, candidates []string) ([]Classification, error) {
	c.candidates = append([]string(nil), candidates...)
	return append([]Classification(nil), c.results...), nil
}

func TestJevFindAllClassifiesWithoutEmbeddings(t *testing.T) {
	t.Parallel()

	store := newSemanticStore(&fakeModel{})
	store.add(1, "old")
	store.add(2, "related")
	store.add(3, "best")
	classifier := &fakeJevClassifier{results: []Classification{
		{Index: 0, Score: 0, Confidence: 1},
		{Index: 1, Score: 2, Confidence: .8},
		{Index: 2, Score: 3, Confidence: .9},
	}}

	got, err := NewJev("query", 2, classifier).FindAll(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(classifier.candidates) != 3 {
		t.Fatalf("classified candidates = %d, want 3", len(classifier.candidates))
	}
	if len(got) != 2 || got[0].Raw[0].GetSummary() != "best" || got[1].Raw[0].GetSummary() != "related" {
		t.Fatalf("FindAll order mismatch: %#v", got)
	}
}

func TestJevCandidateLimitKeepsRecentCandidates(t *testing.T) {
	t.Parallel()

	store := newSemanticStore(&fakeModel{})
	store.add(1, "old")
	store.add(2, "recent")
	classifier := &fakeJevClassifier{results: []Classification{{Index: 0, Score: 3, Confidence: 1}}}

	got, err := NewJev("query", 1, classifier).WithCandidateLimit(1).FindAll(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(classifier.candidates) != 1 || classifier.candidates[0] != "recent" {
		t.Fatalf("classified candidates = %#v", classifier.candidates)
	}
	if got[0].Scope != (view.EntryRange{SeqS: entry.SeqFromUint64(2), SeqE: entry.SeqFromUint64(3)}) {
		t.Fatalf("scope = %#v", got[0].Scope)
	}
}
