package tape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
	jsonlines "github.com/simonfrey/jsonl"
)

// We should treat Tape as the system I/O
// so , we can allow tape to iterate itself via agent sdk's general system I/O operation skill
//
// 得益于这个机制，我们可以让 agent 自己判断什么时候该 handoff , 或者自己梳理上下文
var _ (io.ReadWriteCloser) = (*Tape)(nil)

type JevAnchorer interface {
	Anchor(context.Context, entry.EntryLike, view.EntryRange) (entry.EntryLike, bool, error)
}

// Tape is the agent's backend
type Tape struct {
	storage.TapeStorage

	OwnerID          string
	View             view.EntryRange
	JevAnchorer      JevAnchorer
	OnJevAnchorError func(error)

	storeMu sync.Mutex
	readSeq entry.Seq
	readBuf *bytes.Reader
}

// Store appends an entry, then optionally derives an anchor:jev memory point.
// Jev anchoring is a fail-open post-write index operation: its errors are sent
// to OnJevAnchorError and never make a committed primary entry look uncommitted.
func (t *Tape) Store(ctx context.Context, e entry.EntryLike) error {
	if t.JevAnchorer == nil || e == nil || e.GetKind().IsAnchor() {
		return t.TapeStorage.Store(ctx, e)
	}

	t.storeMu.Lock()
	if err := t.TapeStorage.Store(ctx, e); err != nil {
		t.storeMu.Unlock()
		return err
	}
	seq := e.GetID()
	if seq.IsZero() {
		tv, err := t.Get(ctx)
		if err != nil {
			t.storeMu.Unlock()
			t.reportJevAnchorError(fmt.Errorf("tape: resolve Jev anchor scope: %w", err))
			return nil
		}
		seq = tv.Scope.SeqE
	}
	t.storeMu.Unlock()

	anchor, ok, err := t.JevAnchorer.Anchor(ctx, e, view.EntryRange{SeqS: seq, SeqE: entry.NextEntryID(seq)})
	if err != nil {
		t.reportJevAnchorError(fmt.Errorf("tape: Jev anchor decision: %w", err))
		return nil
	}
	if ok {
		if anchor == nil {
			t.reportJevAnchorError(errors.New("tape: Jev anchorer returned nil anchor"))
			return nil
		}
		t.storeMu.Lock()
		defer t.storeMu.Unlock()
		if err := t.TapeStorage.Store(ctx, anchor); err != nil {
			t.reportJevAnchorError(fmt.Errorf("tape: store Jev anchor: %w", err))
		}
	}
	return nil
}

func (t *Tape) reportJevAnchorError(err error) {
	if t.OnJevAnchorError != nil {
		t.OnJevAnchorError(err)
	}
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
	t.View.SeqE = entry.Seq{}
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

func (t *Tape) SetView(r view.EntryRange) {
	t.View = r
	t.resetReadState()
}

func (t *Tape) context() context.Context {
	return owner.WithOwnerId(context.Background(), t.OwnerID)
}

func (t *Tape) nextEntryView() error {
	ctx := t.context()
	if t.View.SeqE.IsZero() {
		tv, err := t.Get(ctx)
		if err != nil {
			return err
		}
		t.View.SeqE = tv.Scope.SeqE.Next()
	}
	if t.readSeq.IsZero() {
		t.readSeq = t.View.SeqS
	}
	if t.readSeq.Cmp(t.View.SeqE) >= 0 {
		return io.EOF
	}

	ev, err := t.Range(ctx, view.EntryRange{
		SeqS: t.readSeq,
		SeqE: t.readSeq.Next(),
	})
	if err != nil {
		return err
	}
	t.readSeq = t.readSeq.Next()
	if len(ev.Raw) == 0 {
		return t.nextEntryView()
	}

	var data bytes.Buffer
	if err := jsonlines.NewWriter(&data).Write(ev.Raw[0]); err != nil {
		return err
	}
	t.readBuf = bytes.NewReader(data.Bytes())
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
		return t.stringEntry(v)
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

func (t *Tape) stringEntry(text string) (entry.Entry, error) {
	return entry.NewEntry(
		entry.WithEntryContent(text),
		entry.WithEntryOwner(t.OwnerID),
	), nil
}

func (t *Tape) fillEntryDefaults(ctx context.Context, e entry.Entry) (entry.Entry, error) {
	if e.Owner == "" {
		ownerID, err := owner.GetOwnerId(ctx)
		if err == nil {
			e.Owner = ownerID
		}
	}
	return e, nil
}

func (t *Tape) resetReadState() {
	t.readSeq = entry.Seq{}
	t.readBuf = nil
}
