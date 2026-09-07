// Package finder -- 访达
package finder

import (
	"context"
	"errors"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Engine is a search engine represents `How to search(callback in some ways) entries on the tape`.
// Which allows us:
// - uses the better algorithm to find the best-matched entry from tape storage
// - easy to extend tape searching mechanism
// - keep the tape itself simple and clean , and put the complexity away from the storage
type Engine interface {
	Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error)
}

type ByEntryID entry.Seq

// NewByEntryID returns an exact-entry finder for id.
func NewByEntryID(id entry.Seq) ByEntryID {
	return ByEntryID(id)
}

func (id ByEntryID) Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error) {
	seq := entry.Seq(id)
	if seq.IsZero() {
		return view.EntryView{}, errors.New("finder: empty entry id")
	}
	return tape.Range(ctx, view.EntryRange{SeqS: seq, SeqE: seq.Next()})
}
