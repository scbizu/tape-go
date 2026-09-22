package finder

import (
	"context"
	"encoding/json"
	"iter"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fakeJevClassifier struct {
	candidates []entry.JevMemoryState
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
	jevPayload, err := json.Marshal(entry.JevAnchor{
		State: entry.JevMemoryState{Decisions: []string{"archived decision"}},
		SeqS:  entry.SeqFromUint64(4),
		SeqE:  entry.SeqFromUint64(9),
	})
	if err != nil {
		t.Fatal(err)
	}
	jevAnchor := entry.NewAnchor(entry.SeqFromUint64(10), "owner-a", entry.AnchorKindJev, jevPayload)
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

func (c *fakeJevClassifier) Classify(_ context.Context, _ string, candidates []entry.JevMemoryState) iter.Seq2[Classification, error] {
	return func(yield func(Classification, error) bool) {
		c.candidates = append([]entry.JevMemoryState(nil), candidates...)
		for _, result := range c.results {
			if !yield(result, nil) {
				return
			}
		}
	}
}

func TestJevFindAllReturnsBestClassificationWithoutEmbeddings(t *testing.T) {
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

	got, err := NewJev("query", 1, classifier).FindAll(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(classifier.candidates) != 3 {
		t.Fatalf("classified candidates = %d, want 3", len(classifier.candidates))
	}
	if len(got) != 1 || got[0].Raw[0].GetSummary() != "best" {
		t.Fatalf("FindAll result mismatch: %#v", got)
	}
}

func TestJevFindAllReturnsEmptySliceWithoutCandidates(t *testing.T) {
	t.Parallel()

	got, err := NewJev("query", 1, &fakeJevClassifier{}).FindAll(context.Background(), newSemanticStore(&fakeModel{}))
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("FindAll = %#v, want non-nil empty slice", got)
	}
}

func TestJevRequiresTopKOne(t *testing.T) {
	t.Parallel()

	_, err := NewJev("query", 2, &fakeJevClassifier{}).FindAll(context.Background(), newSemanticStore(&fakeModel{}))
	if err == nil {
		t.Fatal("FindAll accepted TopK other than one")
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
	if len(classifier.candidates) != 1 || len(classifier.candidates[0].Decisions) != 1 || classifier.candidates[0].Decisions[0] != "recent" {
		t.Fatalf("classified candidates = %#v", classifier.candidates)
	}
	if got[0].Scope != (view.EntryRange{SeqS: entry.SeqFromUint64(2), SeqE: entry.SeqFromUint64(3)}) {
		t.Fatalf("scope = %#v", got[0].Scope)
	}
}
