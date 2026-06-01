package entry

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
	ID() uint64
	// The kind of the entry
	Kind() EntryKind
	// Entry should have the ability to summarize itself
	// or just keep the raw content
	Summary() string
	// The owner of the entry
	Owner() string
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
		e.ek = ek
	}
}

func WithEntryContent(text string) EntryOption {
	return func(e *Entry) {
		e.text = text
	}
}

func WithEntryOwner(owner string) EntryOption {
	return func(e *Entry) {
		e.owner = owner
	}
}

func WithEntryId(id uint64) EntryOption {
	return func(e *Entry) {
		e.seq = id
	}
}

type Entry struct {
	seq   uint64
	ek    EntryKind
	text  string
	owner string
}

func (e Entry) ID() uint64 {
	return e.seq
}

func (e Entry) Kind() EntryKind {
	return e.ek
}

func (e Entry) Summary() string {
	return e.text
}

func (e Entry) Owner() string {
	return e.owner
}

// CustomEntry is an `entry` that carries with some extensions
type CustomEntry struct {
	Entry
	Extensions map[string]any
}

type EntryView struct{}
