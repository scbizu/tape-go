package jev

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Classification is a JEV relevance judgment for one candidate.
type Classification struct {
	Index      int
	Score      float64
	Confidence float64
}

// Classifier scores the active anchor states without embeddings.
type Classifier interface {
	Classify(context.Context, string, []MemoryState) iter.Seq2[Classification, error]
}

// Finder uses the snapshot owned by one JEV storage decorator.
type Finder struct {
	Query      string
	Classifier Classifier
	storage    *Storage
}

func (j Finder) Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error) {
	views, err := j.FindAll(ctx, tape)
	if err != nil {
		return view.EntryView{}, err
	}
	if len(views) == 0 {
		return view.EntryView{}, nil
	}
	return views[0], nil
}

func (j Finder) FindAll(ctx context.Context, tape storage.EntryStorage) ([]view.EntryView, error) {
	if j.Query == "" || j.Classifier == nil || j.storage == nil {
		return nil, errors.New("jev: finder is not configured")
	}
	snapshot, err := j.storage.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	candidates := snapshot.Anchors
	if len(candidates) == 0 {
		return []view.EntryView{}, nil
	}

	states := make([]MemoryState, len(candidates))
	for i := range candidates {
		states[i] = candidates[i].State
	}
	seen := make(map[int]struct{}, len(candidates))
	var best Classification
	found := false
	for result, err := range j.Classifier.Classify(ctx, j.Query, states) {
		if err != nil {
			return nil, fmt.Errorf("jev: classify: %w", err)
		}
		if result.Index < 0 || result.Index >= len(candidates) {
			return nil, fmt.Errorf("jev: classification index %d out of range", result.Index)
		}
		if _, ok := seen[result.Index]; ok {
			return nil, fmt.Errorf("jev: duplicate classification index %d", result.Index)
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
