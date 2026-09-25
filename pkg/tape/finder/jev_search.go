package finder

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Classification is a Jev relevance judgment for one candidate.
type Classification struct {
	Index      int
	Score      float64
	Confidence float64
}

// JevClassifier evaluates candidates directly, without embeddings.
type JevClassifier interface {
	Classify(context.Context, string, []entry.JevMemoryState) iter.Seq2[Classification, error]
}

// Jev is a classifier-based search engine. It is intentionally independent of
// Semantic: active anchors go directly from the storage snapshot to the classifier.
type Jev struct {
	Query      string        `validate:"required"`
	Classifier JevClassifier `validate:"required"`
}

func NewJev(query string, classifier JevClassifier) Jev {
	return Jev{Query: query, Classifier: classifier}
}

func (j Jev) Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error) {
	views, err := j.FindAll(ctx, tape)
	if err != nil {
		return view.EntryView{}, err
	}
	if len(views) == 0 {
		return view.EntryView{}, nil
	}
	return views[0], nil
}

func (j Jev) FindAll(ctx context.Context, tape storage.EntryStorage) ([]view.EntryView, error) {
	if err := validateStructure("Jev search", j); err != nil {
		return nil, err
	}
	indexedTape := tape
	for {
		unwrapper, ok := indexedTape.(interface{ Unwrap() storage.TapeStorage })
		if !ok {
			break
		}
		indexedTape = unwrapper.Unwrap()
	}
	indexer, ok := indexedTape.(AnchorSnapshotReader)
	if !ok {
		return nil, errors.New("finder: anchor snapshot is not supported")
	}
	snapshot, err := indexer.AnchorSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	candidates := snapshot.Anchors
	if len(candidates) == 0 {
		return []view.EntryView{}, nil
	}

	states := make([]entry.JevMemoryState, len(candidates))
	for i := range candidates {
		states[i] = candidates[i].State
	}
	seen := make(map[int]struct{}, len(candidates))
	var best Classification
	found := false
	for result, err := range j.Classifier.Classify(ctx, j.Query, states) {
		if err != nil {
			return nil, fmt.Errorf("finder: Jev classify: %w", err)
		}
		if result.Index < 0 || result.Index >= len(candidates) {
			return nil, fmt.Errorf("finder: Jev classification index %d out of range", result.Index)
		}
		if _, ok := seen[result.Index]; ok {
			return nil, fmt.Errorf("finder: duplicate Jev classification index %d", result.Index)
		}
		seen[result.Index] = struct{}{}
		if !found || result.Score > best.Score || result.Score == best.Score && result.Confidence > best.Confidence {
			best, found = result, true
		}
	}
	if !found {
		return []view.EntryView{}, nil
	}
	ev, err := tape.Range(ctx, candidates[best.Index].Scope)
	if err != nil {
		return nil, err
	}
	return []view.EntryView{ev}, nil
}
