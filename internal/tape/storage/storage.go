// Package storage defines the interface for tape storage and entry storage.
package storage

import (
	"context"

	"github.com/scbizu/tape-go/internal/tape/entry"
)

type TapeView struct {
	SessionID      string
	HeadAt, TailAt uint64
}

type EntryStorage interface {
	Store(context.Context, entry.Entry) error
	Range(context.Context, Range) ([]entry.EntryView, error)
	// Search is a more abstracted `Range` , maybe for embedding search
	Search(context.Context, ...SearchBy) ([]entry.EntryView, error)
}

type TapeStorage interface {
	Init(context.Context, SessionID) error
	Get(context.Context, SessionID) (TapeView, error)
	// TODO: Mask marks the time-period from a tape as low-priority
	// 往事不堪回首 , 也许我们需要一个机制来定义某些记忆是我们不想记起来的
	// Be fair to agents
	// Mask(context.Context, SessionID, uint64) (TapeView, error)
}

type SessionID string

type Range struct {
	Start uint64
	End   uint64
}

type SearchOption struct {
	viewAssembler Range
}

type SearchBy func(*SearchOption)
