package finder

import (
	"context"
	"errors"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

func TestSemanticFindAllReranksMatches(t *testing.T) {
	t.Parallel()

	store := newSemanticStore(&fakeModel{
		enabled: true,
		vectors: map[string][]float32{
			"query": {1, 0},
			"near":  {1, 0},
			"also":  {1, 0},
			"far":   {0, 1},
		},
		reranked: []string{"also", "near"},
	})
	store.add(1, "near")
	store.add(2, "also")
	store.add(3, "far")

	got, err := NewSemantic("query", 2).FindAll(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("FindAll len: want 2, got %d", len(got))
	}
	if got[0].Raw[0].GetSummary() != "also" || got[1].Raw[0].GetSummary() != "near" {
		t.Fatalf("FindAll order mismatch: got %q, %q", got[0].Raw[0].GetSummary(), got[1].Raw[0].GetSummary())
	}
}

func TestSemanticFindReturnsRangeMatch(t *testing.T) {
	t.Parallel()

	store := newSemanticStore(&fakeModel{
		enabled: true,
		vectors: map[string][]float32{
			"archive": {1, 0},
			"old":     {0, 1},
			"new":     {0, 1},
		},
	})
	store.add(1, "old")
	store.add(2, "new")
	store.index.Items = append(store.index.Items, SemanticItem{
		Summary:   "archive",
		Embedding: []float32{1, 0},
		Scope:     view.EntryRange{SeqS: 1, SeqE: 3},
	})

	got, err := NewSemantic("archive", 1).Find(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if got.Scope != (view.EntryRange{SeqS: 1, SeqE: 3}) || len(got.Raw) != 2 {
		t.Fatalf("Find range mismatch: scope=%+v raw=%d", got.Scope, len(got.Raw))
	}
}

func TestSemanticFindReturnsRerankError(t *testing.T) {
	t.Parallel()

	want := errors.New("rerank failed")
	store := newSemanticStore(&fakeModel{
		enabled:   true,
		vectors:   map[string][]float32{"query": {1, 0}, "hit": {1, 0}},
		rerankErr: want,
	})
	store.add(1, "hit")

	if _, err := NewSemantic("query", 1).FindAll(context.Background(), store); !errors.Is(err, want) {
		t.Fatalf("FindAll rerank error: want %v, got %v", want, err)
	}
}

type semanticStore struct {
	index   SemanticIndex
	entries map[uint64]entry.EntryLike
}

func newSemanticStore(model *fakeModel) *semanticStore {
	return &semanticStore{
		index:   SemanticIndex{Model: model},
		entries: make(map[uint64]entry.EntryLike),
	}
}

func (s *semanticStore) add(id uint64, text string) {
	e := entry.NewEntry(entry.WithEntryID(id), entry.WithEntryContent(text))
	s.entries[id] = e
	embedding, _ := s.index.Model.Embedding(context.Background(), text)
	s.index.Items = append(s.index.Items, SemanticItem{
		Summary:   text,
		Embedding: embedding,
		Scope:     view.EntryRange{SeqS: id, SeqE: id + 1},
	})
}

func (s *semanticStore) Store(context.Context, entry.EntryLike) error {
	return nil
}

func (s *semanticStore) Range(_ context.Context, r view.EntryRange, _ ...storage.RangeBy) (view.EntryView, error) {
	out := view.EntryView{SessionId: "session-a", Owner: "owner-a", Scope: r}
	for seq := r.SeqS; seq < r.SeqE; seq++ {
		if e, ok := s.entries[seq]; ok {
			out.Raw = append(out.Raw, e)
		}
	}
	return out, nil
}

func (s *semanticStore) SemanticIndex(context.Context) (SemanticIndex, error) {
	return s.index, nil
}

type fakeModel struct {
	enabled   bool
	vectors   map[string][]float32
	reranked  []string
	rerankErr error
}

func (m *fakeModel) IsEnable() bool {
	return m.enabled
}

func (m *fakeModel) Embedding(_ context.Context, text string) ([]float32, error) {
	if v, ok := m.vectors[text]; ok {
		return v, nil
	}
	return []float32{0, 1}, nil
}

func (m *fakeModel) ReRank(_ context.Context, _ string, candidates []string) ([]string, error) {
	if m.rerankErr != nil {
		return nil, m.rerankErr
	}
	if m.reranked != nil {
		return m.reranked, nil
	}
	return candidates, nil
}
