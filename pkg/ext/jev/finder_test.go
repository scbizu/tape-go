package jev

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fakeClassifier struct {
	candidates []MemoryState
	results    []Classification
}

func (c *fakeClassifier) Classify(_ context.Context, _ string, candidates []MemoryState) iter.Seq2[Classification, error] {
	return func(yield func(Classification, error) bool) {
		c.candidates = append([]MemoryState(nil), candidates...)
		for _, result := range c.results {
			if !yield(result, nil) {
				return
			}
		}
	}
}

type finderStore struct {
	anchors   []entry.EntryLike
	anchorErr error
}

func (finderStore) Init(context.Context) error { return nil }
func (finderStore) Get(context.Context) (view.TapeView, error) {
	return view.TapeView{Owner: "owner-a", SessionID: "session-a"}, nil
}
func (finderStore) Store(_ context.Context, e entry.EntryLike) (entry.EntryLike, error) {
	return e, nil
}
func (finderStore) Range(_ context.Context, r view.EntryRange, _ ...storage.RangeBy) (view.EntryView, error) {
	return view.EntryView{Scope: r, Raw: []entry.EntryLike{entry.NewEntry(entry.WithEntryID(r.SeqS))}}, nil
}
func (s finderStore) Anchors(context.Context) iter.Seq2[entry.EntryLike, error] {
	return func(yield func(entry.EntryLike, error) bool) {
		for _, anchor := range s.anchors {
			if !yield(anchor, nil) {
				return
			}
		}
		if s.anchorErr != nil {
			yield(nil, s.anchorErr)
		}
	}
}
func (finderStore) Rewind(context.Context, ...storage.RewindBy) (view.EntryRange, error) {
	return view.EntryRange{}, nil
}

func TestFinderUsesBestAnchor(t *testing.T) {
	store := &Storage{
		TapeStorage: finderStore{},
		snapshots: map[snapshotKey]Snapshot{{owner: "owner-a", session: "session-a"}: {
			Anchors: []AnchorRecord{
				{State: MemoryState{Decisions: []string{"old"}}, Scope: view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)}},
				{State: MemoryState{Decisions: []string{"best"}}, Scope: view.EntryRange{SeqS: entry.SeqFromUint64(2), SeqE: entry.SeqFromUint64(3)}},
			},
		}},
	}
	classifier := &fakeClassifier{results: []Classification{{Index: 0, Score: 1}, {Index: 1, Score: 2}}}
	got, err := (Finder{Query: "query", Classifier: classifier, storage: store}).Find(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope.SeqS != entry.SeqFromUint64(2) || len(classifier.candidates) != 2 {
		t.Fatalf("find = %#v, candidates = %#v", got, classifier.candidates)
	}
}

func TestInitDoesNotPublishPartialSnapshot(t *testing.T) {
	anchor, err := NewAnchor(entry.SeqFromUint64(1), "owner-a", Anchor{
		State: MemoryState{Decisions: []string{"durable"}},
		SeqS:  entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := finderStore{anchors: []entry.EntryLike{anchor}, anchorErr: errors.New("read failed")}
	active := &view.EntryView{}
	store, err := NewStorage(base, active, Config{
		Decider: fixedAnchorDecider(.9), Summarizer: fixedSummarizer{Decisions: []string{"durable"}},
		Classifier: &fakeClassifier{}, Threshold: .7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(context.Background()); err == nil {
		t.Fatal("Init accepted a failed anchor enumeration")
	}
	if _, err := store.snapshot(context.Background()); err == nil {
		t.Fatal("Init published a partial snapshot")
	}
}
