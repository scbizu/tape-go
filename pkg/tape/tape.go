package tape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

var _ (io.ReadWriteCloser) = (*Tape)(nil)

// Tape is the agent's backend
type Tape struct {
	storage.TapeStorage

	OwnerID string
	View    view.EntryRange

	readSeq uint64
	readBuf *bytes.Reader
}

// Read reads out to `p` as the entry (entries for batch approach ?) bytes.
func (t *Tape) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	total := 0
	for total < len(p) {
		if t.readBuf == nil || t.readBuf.Len() == 0 {
			if err := t.nextEntryView(); err != nil {
				if errors.Is(err, io.EOF) && total > 0 {
					return total, nil
				}
				return total, err
			}
		}

		n, err := t.readBuf.Read(p[total:])
		total += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				t.readBuf = nil
				continue
			}
			return total, err
		}
	}
	return total, nil
}

// Write appends `p` (entry bytes) as entry to tape.
func (t *Tape) Write(p []byte) (int, error) {
	e, err := t.entryFromBytes(p)
	if err != nil {
		return 0, err
	}
	if err := t.Store(t.context(), e); err != nil {
		return 0, err
	}
	t.readBuf = nil
	return len(p), nil
}

// Close closes tape append window , maybe:
// - A Database conn
// - A File handler
func (t *Tape) Close() error {
	if closer, ok := t.TapeStorage.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (t *Tape) WriteEntry(ctx context.Context) error {
	return nil
}

func (t *Tape) context() context.Context {
	return owner.WithOwnerId(context.Background(), t.OwnerID)
}

func (t *Tape) nextEntryView() error {
	ctx := t.context()
	if t.View.SeqE == 0 {
		tv, err := t.Get(ctx)
		if err != nil {
			return err
		}
		t.View.SeqE = entry.NextEntryID(tv.Scope.SeqE)
	}
	if t.readSeq == 0 {
		t.readSeq = t.View.SeqS
	}
	if t.readSeq >= t.View.SeqE {
		return io.EOF
	}

	ev, err := t.Range(ctx, view.EntryRange{
		SeqS: t.readSeq,
		SeqE: entry.NextEntryID(t.readSeq),
	})
	if err != nil {
		return err
	}
	t.readSeq++
	if len(ev.Raw) == 0 {
		return t.nextEntryView()
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	t.readBuf = bytes.NewReader(data)
	return nil
}

func (t *Tape) entryFromBytes(p []byte) (entry.Entry, error) {
	ctx := t.context()

	var raw any
	if err := json.Unmarshal(p, &raw); err != nil {
		return entry.Entry{}, fmt.Errorf("tape: decode write payload: %w", err)
	}

	switch v := raw.(type) {
	case string:
		return t.stringEntry(ctx, v)
	case map[string]any:
		var e entry.Entry
		if err := json.Unmarshal(p, &e); err != nil {
			return entry.Entry{}, fmt.Errorf("tape: %w", err)
		}
		return t.fillEntryDefaults(ctx, e)
	default:
		return entry.Entry{}, fmt.Errorf("tape: unsupported write payload type %T", raw)
	}
}

func (t *Tape) stringEntry(ctx context.Context, text string) (entry.Entry, error) {
	tv, err := t.Get(ctx)
	if err != nil {
		return entry.Entry{}, err
	}
	return entry.NewEntry(
		entry.WithEntryID(
			entry.NextEntryID(tv.Scope.SeqE),
		),
		entry.WithEntryContent(text),
		entry.WithEntryOwner(tv.Owner),
	), nil
}

func (t *Tape) fillEntryDefaults(ctx context.Context, e entry.Entry) (entry.Entry, error) {
	if e.Seq == 0 {
		tv, err := t.Get(ctx)
		if err != nil {
			return entry.Entry{}, err
		}
		e.Seq = entry.NextEntryID(tv.Scope.SeqE)
		if e.Owner == "" {
			e.Owner = tv.Owner
		}
	}
	if e.Owner == "" {
		ownerID, err := owner.GetOwnerId(ctx)
		if err == nil {
			e.Owner = ownerID
		}
	}
	return e, nil
}
