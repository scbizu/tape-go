package entry

import "sync/atomic"

type EntryKind string

const (
	EntryUser       EntryKind = "user"
	EntryAssistant  EntryKind = "assistant"
	EntryToolCall   EntryKind = "tool_call"
	EntryToolResult EntryKind = "tool_result"
	EntrySystem     EntryKind = "system"
)

// EntryLike is the duck-type interface for entries
type EntryLike interface {
	// The range-able or hash-able ID of the entry
	GetID() uint64
	// The kind of the entry
	GetKind() EntryKind
	// Entry should have the ability to summarize itself
	// or just keep the raw content
	GetSummary() string
	// The owner of the entry
	GetOwner() string
}

var (
	_ EntryLike = (*Entry)(nil)
	_ EntryLike = (*CustomEntry)(nil)
)

func NewEntry(
	opts ...EntryOption,
) Entry {
	var e Entry
	for _, opt := range opts {
		opt(&e)
	}
	return e
}

type EntryOption func(*Entry)

func WithEntryKind(ek EntryKind) EntryOption {
	return func(e *Entry) {
		e.Ek = ek
	}
}

func WithEntryContent(text string) EntryOption {
	return func(e *Entry) {
		e.Text = text
	}
}

func WithEntryOwner(owner string) EntryOption {
	return func(e *Entry) {
		e.Owner = owner
	}
}

func WithEntryID(id uint64) EntryOption {
	return func(e *Entry) {
		e.Seq = id
	}
}

func NextEntryID(old uint64) uint64 {
	return atomic.AddUint64(&old, +1)
}

type Entry struct {
	Seq   uint64
	Ek    EntryKind
	Text  string
	Owner string
}

func (e Entry) GetID() uint64 {
	return e.Seq
}

func (e Entry) GetKind() EntryKind {
	return e.Ek
}

func (e Entry) GetSummary() string {
	return e.Text
}

func (e Entry) GetOwner() string {
	return e.Owner
}

// CustomEntry is an `entry` that carries with some extensions
type CustomEntry struct {
	Entry
	Extensions map[string]any
}
