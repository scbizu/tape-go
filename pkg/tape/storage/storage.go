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
	// Search is a more abstracted `Range`
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
	// eId denotes for entry id or `seq` in most of our codebase
	// It is the simple but the most stable search mechanism to get entry data
	eId uint64
	// semanticText here mainly denotes for embedding search
	// It works together with entry/anchor `summary`
	semanticText string
	// fullText here mainly denotes for fulltext search mechanism
	// It maybe efficient and cache-friendly for some scenarios that data was less-modified like coding-agent
	// According to [tomiya's Obelisk](https://github.com/tommy0103/obelisk) Research.
	fullText string
}

func (so SearchOption) GetEntryId() uint64 {
	return so.eId
}

func (so SearchOption) GetSemanticText() string {
	return so.semanticText
}

func (so SearchOption) GetFullText() string {
	return so.fullText
}

type SearchBy func(*SearchOption)

func WithEntryId(eId uint64) SearchBy {
	return func(so *SearchOption) {
		so.eId = eId
	}
}

func WithSemanticPrompt(text string) SearchBy {
	return func(so *SearchOption) {
		so.semanticText = text
	}
}

func WithFullText(fullText string) SearchBy {
	return func(so *SearchOption) {
		so.fullText = fullText
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
