// Package steer provides a persistent inbox backed by an existing Tape.
package steer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const (
	queuedKind entry.EntryKind = "steer:queued"
	ackedKind  entry.EntryKind = "steer:acked"
)

// Message is an external user input. ID is local to its Inbox.
type Message struct {
	ID   entry.Seq
	Text string
}

// Inbox stores enqueue and acknowledgement records on a caller-supplied Tape.
// Enqueue, Pending and Ack can be called concurrently.
type Inbox struct {
	store   storage.TapeStorage
	ownerID string

	mu      sync.Mutex
	head    entry.Seq
	pending []Message
}

// NewInbox binds an initialized Tape owner and session. Use a scope independent
// of the Main Tape and share the storage handle with other concurrent writers.
func NewInbox(store storage.TapeStorage, ownerID string) (*Inbox, error) {
	if store == nil || ownerID == "" {
		return nil, errors.New("steer: storage and owner ID are required")
	}
	return &Inbox{store: store, ownerID: ownerID}, nil
}

// Enqueue durably appends text and returns its assigned message ID.
func (i *Inbox) Enqueue(ctx context.Context, text string) (entry.Seq, error) {
	if strings.TrimSpace(text) == "" {
		return entry.Seq{}, errors.New("steer: empty input")
	}
	stored, err := i.append(ctx, queuedKind, text)
	if err != nil {
		return entry.Seq{}, err
	}
	return stored.GetID(), nil
}

// Pending returns a snapshot of all unacknowledged messages in enqueue order.
func (i *Inbox) Pending(ctx context.Context) ([]Message, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	ctx = owner.WithOwnerId(ctx, i.ownerID)
	tapeView, err := i.store.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("steer: read inbox head: %w", err)
	}
	head := tapeView.Scope.SeqE
	if head.IsZero() {
		return nil, nil
	}
	if head == i.head {
		return slices.Clone(i.pending), nil
	}

	entries, err := i.store.Range(ctx, view.EntryRange{
		SeqS: i.head.Next(),
		SeqE: head.Next(),
	})
	if err != nil {
		return nil, fmt.Errorf("steer: read inbox tail: %w", err)
	}
	messages := i.pending
	acked := make(map[entry.Seq]bool)
	for _, e := range entries.Raw {
		switch e.GetKind() {
		case queuedKind:
			messages = append(messages, Message{ID: e.GetID(), Text: e.GetSummary()})
			// Acknowledgements apply to messages already queued when written.
			delete(acked, e.GetID())
		case ackedKind:
			id, err := entry.ParseSeq(e.GetSummary())
			if err != nil {
				return nil, fmt.Errorf("steer: decode acknowledgement at entry %s: %w", e.GetID(), err)
			}
			acked[id] = true
		}
	}
	if len(acked) > 0 {
		messages = slices.DeleteFunc(messages, func(m Message) bool { return acked[m.ID] })
	}
	i.pending = messages
	i.head = head
	return slices.Clone(i.pending), nil
}

// Ack appends an acknowledgement. Pass an ID returned by this Inbox's Enqueue
// or Pending. Repeated acknowledgements have the same effect on Pending.
func (i *Inbox) Ack(ctx context.Context, id entry.Seq) error {
	if id.IsZero() {
		return errors.New("steer: empty message ID")
	}
	_, err := i.append(ctx, ackedKind, id.String())
	return err
}

func (i *Inbox) append(ctx context.Context, kind entry.EntryKind, text string) (entry.EntryLike, error) {
	stored, err := i.store.Store(owner.WithOwnerId(ctx, i.ownerID), entry.NewEntry(
		entry.WithEntryKind(kind),
		entry.WithEntryOwner(i.ownerID),
		entry.WithEntryContent(text),
	))
	if err != nil {
		return nil, fmt.Errorf("steer: store %s: %w", kind, err)
	}
	return stored, nil
}
