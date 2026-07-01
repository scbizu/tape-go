// Package finder -- 访达
package finder

import (
	"context"
	"errors"

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

type ByEntryID uint64

func (id ByEntryID) Find(ctx context.Context, tape storage.EntryStorage) (view.EntryView, error) {
	if id == 0 {
		return view.EntryView{}, errors.New("finder: empty entry id")
	}
	return tape.Range(ctx, view.EntryRange{SeqS: uint64(id), SeqE: uint64(id) + 1})
}
