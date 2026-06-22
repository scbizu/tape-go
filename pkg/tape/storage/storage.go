// Package storage defines the interface for tape storage and entry storage.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

var ErrNoAnchor = errors.New("storage: no anchor")

type EntryStorage interface {
	Store(context.Context, entry.EntryLike) error
	Range(context.Context, view.EntryRange, ...RangeBy) (view.EntryView, error)
	// Search is a more abstracted `Range` , maybe for embedding search
	Search(context.Context, ...SearchBy) (view.EntryView, error)
}

type TapeStorage interface {
	Init(context.Context) error
	Get(context.Context) (view.TapeView, error)
	// Rewind gets the latest `anchor` context from `seq` back to the current context window
	Rewind(ctx context.Context, opts ...RewindBy) (view.EntryRange, error)
	// TODO: Mask marks the time-period from a tape as low-priority
	// 往事不堪回首 , 也许我们需要一个机制来定义某些记忆是我们不想记起来的
	// Be fair to agents
	// Mask(context.Context, SessionID, uint64) (TapeView, error)
	//
	// TapeStorage should also hold the storage of entries
	EntryStorage
}

type SessionID string

type RangeOption struct {
	After time.Time
}

type RangeBy func(*RangeOption)

func WithRangeAfter(after time.Time) RangeBy {
	return func(option *RangeOption) {
		option.After = after
	}
}

type SearchOption struct {
	eId            string
	semanticPrompt string
}

type SearchBy func(*SearchOption)

func WithEntryId(eId string) SearchBy {
	return func(so *SearchOption) {
		so.eId = eId
	}
}

func WithSemanticPrompt(prompt string) SearchBy {
	return func(so *SearchOption) {
		so.semanticPrompt = prompt
	}
}

type RewindOption struct {
	// fromSeq introduces rewind from e index
	FromSeq uint64
	// maxAnchors represents max anchors to rewind
	// by default, Rewind only rewinds to latest anchor
	MaxAnchors uint8
}

type RewindBy func(*RewindOption)

func WithRewindFromSeq(seq uint64) RewindBy {
	return func(ro *RewindOption) {
		ro.FromSeq = seq
	}
}

func WithRewindMaxAnchors(n uint8) RewindBy {
	return func(ro *RewindOption) {
		ro.MaxAnchors = n
	}
}
