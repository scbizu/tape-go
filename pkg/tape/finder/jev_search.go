package finder

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"sort"

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
// Semantic: candidates go directly from the tape index to the classifier.
type Jev struct {
	Query          string
	TopK           int
	CandidateLimit int
	Classifier     JevClassifier
}

func NewJev(query string, topK int, classifier JevClassifier) Jev {
	return Jev{Query: query, TopK: topK, Classifier: classifier}
}

// WithCandidateLimit limits classification to the most recent candidates.
// Zero keeps the complete candidate index.
func (j Jev) WithCandidateLimit(limit int) Jev {
	j.CandidateLimit = limit
	return j
}

func (j Jev) Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error) {
	views, err := j.FindAll(ctx, tape)
	if err != nil {
		return view.EntryView{}, err
	}
	out := view.EntryView{}
	for i, entryView := range views {
		if i == 0 || entryView.Scope.SeqS.Cmp(out.Scope.SeqS) < 0 {
			out.Scope.SeqS = entryView.Scope.SeqS
		}
		if entryView.Scope.SeqE.Cmp(out.Scope.SeqE) > 0 {
			out.Scope.SeqE = entryView.Scope.SeqE
		}
		if out.SessionId == "" {
			out.SessionId = entryView.SessionId
		}
		if out.Owner == "" {
			out.Owner = entryView.Owner
		}
		out.Raw = append(out.Raw, entryView.Raw...)
	}
	return out, nil
}

func (j Jev) FindAll(ctx context.Context, tape storage.EntryStorage) ([]view.EntryView, error) {
	if j.Query == "" {
		return nil, errors.New("finder: empty Jev query")
	}
	if j.TopK <= 0 {
		return nil, errors.New("finder: invalid Jev topK")
	}
	if j.CandidateLimit < 0 {
		return nil, errors.New("finder: invalid Jev candidate limit")
	}
	if j.Classifier == nil {
		return nil, errors.New("finder: nil Jev classifier")
	}
	indexedTape := tape
	for {
		unwrapper, ok := indexedTape.(interface{ Unwrap() storage.TapeStorage })
		if !ok {
			break
		}
		indexedTape = unwrapper.Unwrap()
	}
	indexer, ok := indexedTape.(CandidateIndexer)
	if !ok {
		return nil, errors.New("finder: candidate index is not supported")
	}
	candidates, err := indexer.CandidateIndex(ctx)
	if err != nil {
		return nil, err
	}
	if j.CandidateLimit > 0 && len(candidates) > j.CandidateLimit {
		candidates = candidates[len(candidates)-j.CandidateLimit:]
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	states := make([]entry.JevMemoryState, len(candidates))
	for i := range candidates {
		states[i] = candidates[i].State
	}
	classified := make([]Classification, 0, len(candidates))
	for result, err := range j.Classifier.Classify(ctx, j.Query, states) {
		if err != nil {
			return nil, fmt.Errorf("finder: Jev classify: %w", err)
		}
		classified = append(classified, result)
	}
	seen := make(map[int]struct{}, len(classified))
	for _, result := range classified {
		if result.Index < 0 || result.Index >= len(candidates) {
			return nil, fmt.Errorf("finder: Jev classification index %d out of range", result.Index)
		}
		if _, ok := seen[result.Index]; ok {
			return nil, fmt.Errorf("finder: duplicate Jev classification index %d", result.Index)
		}
		seen[result.Index] = struct{}{}
	}
	sort.SliceStable(classified, func(i, k int) bool {
		if classified[i].Score == classified[k].Score {
			return classified[i].Confidence > classified[k].Confidence
		}
		return classified[i].Score > classified[k].Score
	})

	limit := min(j.TopK, len(classified))
	out := make([]view.EntryView, 0, limit)
	for _, result := range classified[:limit] {
		ev, err := tape.Range(ctx, candidates[result.Index].Scope)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}
