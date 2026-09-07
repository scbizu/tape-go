package entry

import (
	"strings"
	"time"
)

type EntryKind string

const (
	EntryUser       EntryKind = "user"
	EntryAssistant  EntryKind = "assistant"
	EntryToolCall   EntryKind = "tool_call"
	EntryToolResult EntryKind = "tool_result"
	EntrySystem     EntryKind = "system"
	EntryAnchor     EntryKind = "anchor"
)

func (k EntryKind) IsAnchor() bool {
	return k == EntryAnchor || strings.HasPrefix(string(k), string(EntryAnchor)+":")
}

// EntryLike is the duck-type interface for entries
type EntryLike interface {
	// The range-able or hash-able ID of the entry
	GetID() Seq
	// WithID returns the entry with the given ID.
	WithID(Seq) EntryLike
	// The kind of the entry
	GetKind() EntryKind
	// Entry should have the ability to summarize itself
	// or just keep the raw content
	GetSummary() string
	// The owner of the entry
	GetOwner() string
	// The time when the entry was created
	GetTimestamp() time.Time
	// WithTimestamp returns the entry with the given timestamp.
	WithTimestamp(time.Time) EntryLike
}

var (
	_ EntryLike = (*Entry)(nil)
	_ EntryLike = (*CustomEntry)(nil)
)

func NewEntry(
	opts ...EntryOption,
) Entry {
	e := Entry{Timestamp: time.Now()}
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

func WithEntryID(id Seq) EntryOption {
	return func(e *Entry) {
		e.Seq = id
	}
}

func WithEntryTimestamp(timestamp time.Time) EntryOption {
	return func(e *Entry) {
		e.Timestamp = timestamp
	}
}

// NextEntryID returns old.Next.
// Deprecated: use Seq.Next directly.
func NextEntryID(old Seq) Seq {
	return old.Next()
}

type Entry struct {
	Seq       Seq
	Ek        EntryKind
	Text      string
	Owner     string
	Timestamp time.Time
}

func (e Entry) GetID() Seq {
	return e.Seq
}

func (e Entry) WithID(id Seq) EntryLike {
	e.Seq = id
	return e
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

func (e Entry) GetTimestamp() time.Time {
	return e.Timestamp
}

func (e Entry) WithTimestamp(timestamp time.Time) EntryLike {
	e.Timestamp = timestamp
	return e
}

// CustomEntry is an `entry` that carries with some extensions
type CustomEntry struct {
	Entry
	Extensions map[string]any
}

func (e CustomEntry) WithID(id Seq) EntryLike {
	e.Seq = id
	return e
}

func (e CustomEntry) WithTimestamp(timestamp time.Time) EntryLike {
	e.Timestamp = timestamp
	return e
}
