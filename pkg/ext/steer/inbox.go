// Package steer provides a persistent inbox backed by a Sidecar Tape.
package steer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
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
	sidecar *tape.Tape

	mu      sync.Mutex
	head    entry.Seq
	pending []Message
}

// NewInbox binds an initialized Tape with its own session and the Main Tape's
// owner. The caller manages the Sidecar Tape's lifecycle.
func NewInbox(sidecar *tape.Tape) (*Inbox, error) {
	if sidecar == nil || sidecar.OwnerID == "" {
		return nil, errors.New("steer: sidecar tape with an owner ID is required")
	}
	return &Inbox{sidecar: sidecar}, nil
}

// Tape returns the Inbox's Sidecar Tape.
func (i *Inbox) Tape() *tape.Tape { return i.sidecar }

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

	ctx = owner.WithOwnerId(ctx, i.sidecar.OwnerID)
	tapeView, err := i.sidecar.Get(ctx)
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

	entries, err := i.sidecar.Range(ctx, view.EntryRange{
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
	ownerID := i.sidecar.OwnerID
	stored, err := i.sidecar.Store(owner.WithOwnerId(ctx, ownerID), entry.NewEntry(
		entry.WithEntryKind(kind),
		entry.WithEntryOwner(ownerID),
		entry.WithEntryContent(text),
	))
	if err != nil {
		return nil, fmt.Errorf("steer: store %s: %w", kind, err)
	}
	return stored, nil
}
