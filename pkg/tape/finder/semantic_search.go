package finder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type Semantic struct {
	Query string
	TopK  int
}

func NewSemantic(query string, topK int) Semantic {
	return Semantic{Query: query, TopK: topK}
}

func SemanticPrompt(query string) Semantic {
	return NewSemantic(query, 3)
}

type SemanticIndexer interface {
	SemanticIndex(context.Context) (SemanticIndex, error)
}

type SemanticIndex struct {
	Model llm.Model
	Items []SemanticItem
}

type SemanticItem struct {
	Summary   string
	Embedding []float32
	Scope     view.EntryRange
}

func (s Semantic) Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error) {
	views, err := s.FindAll(ctx, tape)
	if err != nil {
		return view.EntryView{}, err
	}
	out := view.EntryView{}
	for i, entryView := range views {
		if i == 0 || entryView.Scope.SeqS < out.Scope.SeqS {
			out.Scope.SeqS = entryView.Scope.SeqS
		}
		if entryView.Scope.SeqE > out.Scope.SeqE {
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

func (s Semantic) FindAll(ctx context.Context, tape storage.EntryStorage) ([]view.EntryView, error) {
	if s.Query == "" {
		return nil, errors.New("finder: empty semantic query")
	}
	if s.TopK <= 0 {
		return nil, errors.New("finder: invalid topK")
	}
	indexer, ok := tape.(SemanticIndexer)
	if !ok {
		return nil, errors.New("finder: semantic index is not supported")
	}
	index, err := indexer.SemanticIndex(ctx)
	if err != nil {
		return nil, err
	}
	if index.Model == nil || !index.Model.IsEnable() {
		return nil, errors.New("finder: semantic index is not enabled")
	}
	if len(index.Items) == 0 {
		return nil, nil
	}

	query, err := index.Model.Embedding(ctx, s.Query)
	if err != nil {
		return nil, fmt.Errorf("finder: embedding query: %w", err)
	}
	candidates := topSemanticItems(query, index.Items, s.TopK)
	if len(candidates) == 0 {
		return nil, nil
	}

	summaries := make([]string, 0, len(candidates))
	bySummary := make(map[string][]SemanticItem, len(candidates))
	for _, candidate := range candidates {
		summaries = append(summaries, candidate.Summary)
		bySummary[candidate.Summary] = append(bySummary[candidate.Summary], candidate)
	}
	ranked, err := index.Model.ReRank(ctx, s.Query, summaries)
	if err != nil {
		return nil, fmt.Errorf("finder: rerank: %w", err)
	}

	out := make([]view.EntryView, 0, len(ranked))
	used := 0
	for _, summary := range ranked {
		queue := bySummary[summary]
		if len(queue) == 0 {
			continue
		}
		candidate := queue[0]
		bySummary[summary] = queue[1:]
		ev, err := tape.Range(ctx, candidate.Scope)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
		used++
		if used == s.TopK {
			break
		}
	}
	return out, nil
}

func topSemanticItems(query []float32, items []SemanticItem, topK int) []SemanticItem {
	type scoredItem struct {
		item  SemanticItem
		score float64
	}
	scored := make([]scoredItem, 0, len(items))
	for _, item := range items {
		score, ok := cosine(query, item.Embedding)
		if ok {
			scored = append(scored, scoredItem{item: item, score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})
	if len(scored) > topK {
		scored = scored[:topK]
	}
	out := make([]SemanticItem, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.item)
	}
	return out
}

func cosine(a, b []float32) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot, normA, normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0, false
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), true
}
