// Package storage defines the interface for tape storage and entry storage.
package storage

import (
	"context"
	"errors"
	"iter"
	"time"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

var ErrNoAnchor = errors.New("storage: no anchor")

type EntryStorage interface {
	// Store reports the entry as persisted, including its assigned ID.
	Store(context.Context, entry.EntryLike) (entry.EntryLike, error)
	Range(context.Context, view.EntryRange, ...RangeBy) (view.EntryView, error)
}

type TapeStorage interface {
	Init(context.Context) error
	Get(context.Context) (view.TapeView, error)
	// Anchors enumerates the persisted anchors for the current owner and session
	// in ascending entry ID order. An error is yielded once before iteration ends.
	Anchors(context.Context) iter.Seq2[entry.EntryLike, error]
	// Rewind gets the latest `anchor` context from `seq` back to the current context window
	Rewind(ctx context.Context, opts ...RewindBy) (view.EntryRange, error)
	// TODO: Mask marks the time-period from a tape as low-priority
	// 往事不堪回首 , 也许我们需要一个机制来定义某些记忆是我们不想记起来的
	// Be fair to agents
	// Mask(context.Context, SessionID, entry.Seq) (TapeView, error)
	//
	// TapeStorage should also hold the storage of entries
	EntryStorage
}

// Unwrapper exposes the next storage in a decorator chain.
type Unwrapper interface {
	Unwrap() TapeStorage
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

type RewindOption struct {
	// fromSeq introduces rewind from e index
	FromSeq entry.Seq
	// maxAnchors represents max anchors to rewind
	// by default, Rewind only rewinds to latest anchor
	MaxAnchors uint8
}

type RewindBy func(*RewindOption)

func WithRewindFromSeq(seq entry.Seq) RewindBy {
	return func(ro *RewindOption) {
		ro.FromSeq = seq
	}
}

func WithRewindMaxAnchors(n uint8) RewindBy {
	return func(ro *RewindOption) {
		ro.MaxAnchors = n
	}
}
