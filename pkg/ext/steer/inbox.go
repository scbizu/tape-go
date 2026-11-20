// Package steer provides a persistent inbox backed by an existing Tape.
package steer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const (
	queuedKind entry.EntryKind = "steer:queued"
	ackedKind  entry.EntryKind = "steer:acked"
)

// ErrUnknownID indicates that an ID does not identify an enqueue in this Inbox.
var ErrUnknownID = errors.New("steer: unknown message ID")

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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ctx = owner.WithOwnerId(ctx, i.ownerID)
	head, err := i.store.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("steer: read inbox head: %w", err)
	}
	entries, err := i.store.Range(ctx, view.EntryRange{
		SeqS: entry.SeqFromUint64(1),
		SeqE: head.Scope.SeqE.Next(),
	})
	if err != nil {
		return nil, fmt.Errorf("steer: read inbox: %w", err)
	}
	var messages []Message
	acked := make(map[entry.Seq]bool)
	for _, e := range entries.Raw {
		switch e.GetKind() {
		case queuedKind:
			messages = append(messages, Message{ID: e.GetID(), Text: e.GetSummary()})
		case ackedKind:
			id, err := entry.ParseSeq(e.GetSummary())
			if err != nil || id.IsZero() || id.Cmp(e.GetID()) >= 0 {
				return nil, fmt.Errorf("steer: invalid acknowledgement at entry %s", e.GetID())
			}
			acked[id] = true
		}
	}
	messages = slices.DeleteFunc(messages, func(m Message) bool { return acked[m.ID] })
	slices.SortFunc(messages, func(a, b Message) int { return a.ID.Cmp(b.ID) })
	return messages, nil
}

// Ack confirms one enqueued message. Repeated calls have the same effect on
// Pending, even if they append more than one acknowledgement record.
func (i *Inbox) Ack(ctx context.Context, id entry.Seq) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if id.IsZero() {
		return ErrUnknownID
	}
	entries, err := i.store.Range(owner.WithOwnerId(ctx, i.ownerID), view.EntryRange{SeqS: id, SeqE: id.Next()})
	if err != nil {
		return fmt.Errorf("steer: read enqueue %s: %w", id, err)
	}
	if len(entries.Raw) != 1 || entries.Raw[0].GetKind() != queuedKind {
		return fmt.Errorf("%w: %s", ErrUnknownID, id)
	}
	_, err = i.append(ctx, ackedKind, id.String())
	return err
}

func (i *Inbox) append(ctx context.Context, kind entry.EntryKind, text string) (entry.EntryLike, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
