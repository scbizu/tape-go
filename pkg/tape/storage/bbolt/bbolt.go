// Package bbolt provides a bbolt-backed TAPE storage.
package bbolt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
	bolt "go.etcd.io/bbolt"
)

var _ storage.TapeStorage = (*Bbolt)(nil)

var (
	entriesBucket = []byte("entries")
	anchorsBucket = []byte("anchors")
	metaBucket    = []byte("meta")
	stateKey      = []byte("state")
)

type Bbolt struct {
	db        *bolt.DB
	path      string
	sessionID string
}

type metaState struct {
	LastSeq       uint64
	LastTimestamp time.Time
}

func NewBboltStorage(sessionID, path string) (*Bbolt, error) {
	if path == "" {
		return nil, fmt.Errorf("bbolt: empty filepath")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("bbolt: bad filepath: %w", err)
	}
	return &Bbolt{path: abs, sessionID: sessionID}, nil
}

func (b *Bbolt) Init(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return fmt.Errorf("bbolt: init: %w", err)
	}
	if b.db == nil {
		db, err := bolt.Open(b.path, 0o600, &bolt.Options{Timeout: time.Second})
		if err != nil {
			return fmt.Errorf("bbolt: open: %w", err)
		}
		b.db = db
	}
	ownerID, err := owner.GetOwnerId(ctx)
	if err != nil {
		return err
	}
	return b.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{entriesBucket, anchorsBucket, metaBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return fmt.Errorf("create bucket %s: %w", name, err)
			}
		}
		_, err := sessionBucket(tx, metaBucket, ownerID, b.sessionID, true)
		return err
	})
}

func (b *Bbolt) Close() error {
	if b.db == nil {
		return nil
	}
	err := b.db.Close()
	b.db = nil
	return err
}

func (b *Bbolt) Get(ctx context.Context) (view.TapeView, error) {
	ownerID, err := owner.GetOwnerId(ctx)
	if err != nil {
		return view.TapeView{}, err
	}
	state, err := b.readMeta(ownerID)
	if err != nil {
		return view.TapeView{}, err
	}
	return view.TapeView{
		SessionID: b.sessionID,
		Owner:     ownerID,
		Scope:     view.EntryRange{SeqE: state.LastSeq},
	}, nil
}

func (b *Bbolt) Store(ctx context.Context, e entry.EntryLike) error {
	if e == nil {
		return errors.New("bbolt: nil entry")
	}
	ownerID, err := owner.GetOwnerId(ctx)
	if err != nil {
		return err
	}
	if b.db == nil {
		return errors.New("bbolt: storage is not initialized")
	}
	return b.db.Update(func(tx *bolt.Tx) error {
		entries, err := sessionBucket(tx, entriesBucket, ownerID, b.sessionID, true)
		if err != nil {
			return err
		}
		anchors, err := sessionBucket(tx, anchorsBucket, ownerID, b.sessionID, true)
		if err != nil {
			return err
		}
		meta, err := sessionBucket(tx, metaBucket, ownerID, b.sessionID, true)
		if err != nil {
			return err
		}
		state, err := decodeMeta(meta.Get(stateKey))
		if err != nil {
			return err
		}
		if e.GetID() == 0 {
			e = e.WithID(state.LastSeq + 1)
		}
		timestamp := e.GetTimestamp()
		if timestamp.IsZero() {
			timestamp = time.Now()
		}
		if !timestamp.After(state.LastTimestamp) {
			timestamp = state.LastTimestamp.Add(time.Nanosecond)
		}
		e = e.WithTimestamp(timestamp)
		data, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("bbolt: encode entry: %w", err)
		}
		key := seqKey(e.GetID())
		if err := entries.Put(key, data); err != nil {
			return fmt.Errorf("bbolt: store entry: %w", err)
		}
		if e.GetKind().IsAnchor() {
			if err := anchors.Put(key, data); err != nil {
				return fmt.Errorf("bbolt: store anchor: %w", err)
			}
		}
		if e.GetID() > state.LastSeq {
			state.LastSeq = e.GetID()
		}
		state.LastTimestamp = timestamp
		return putMeta(meta, state)
	})
}

func (b *Bbolt) Range(ctx context.Context, r view.EntryRange, opts ...storage.RangeBy) (view.EntryView, error) {
	var option storage.RangeOption
	for _, opt := range opts {
		if opt != nil {
			opt(&option)
		}
	}
	if r.SeqS > r.SeqE {
		return view.EntryView{}, fmt.Errorf("bbolt: invalid range [%d,%d)", r.SeqS, r.SeqE)
	}
	ownerID, err := owner.GetOwnerId(ctx)
	if err != nil {
		return view.EntryView{}, err
	}
	out := view.EntryView{SessionId: b.sessionID, Owner: ownerID, Scope: r}
	if r.SeqS == r.SeqE {
		return out, nil
	}
	if b.db == nil {
		return view.EntryView{}, errors.New("bbolt: storage is not initialized")
	}
	err = b.db.View(func(tx *bolt.Tx) error {
		entries, err := sessionBucket(tx, entriesBucket, ownerID, b.sessionID, false)
		if err != nil || entries == nil {
			return err
		}
		c := entries.Cursor()
		end := seqKey(r.SeqE)
		for k, v := c.Seek(seqKey(r.SeqS)); k != nil && bytes.Compare(k, end) < 0; k, v = c.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, err := decodeEntry(v)
			if err != nil {
				return err
			}
			if option.After.IsZero() || !e.GetTimestamp().Before(option.After) {
				out.Raw = append(out.Raw, e)
			}
		}
		return nil
	})
	return out, err
}

func (b *Bbolt) Rewind(ctx context.Context, opts ...storage.RewindBy) (view.EntryRange, error) {
	option := storage.RewindOption{MaxAnchors: 1}
	for _, opt := range opts {
		if opt != nil {
			opt(&option)
		}
	}
	if option.MaxAnchors == 0 {
		option.MaxAnchors = 1
	}
	seq := option.FromSeq
	if seq == 0 {
		seq = ^uint64(0)
	}
	ownerID, err := owner.GetOwnerId(ctx)
	if err != nil {
		return view.EntryRange{}, err
	}
	if b.db == nil {
		return view.EntryRange{}, errors.New("bbolt: storage is not initialized")
	}
	var result view.EntryRange
	found := uint8(0)
	err = b.db.View(func(tx *bolt.Tx) error {
		anchors, err := sessionBucket(tx, anchorsBucket, ownerID, b.sessionID, false)
		if err != nil || anchors == nil {
			return err
		}
		c := anchors.Cursor()
		k, v := c.Last()
		if seq != ^uint64(0) {
			k, v = c.Seek(seqKey(seq))
			if k == nil {
				k, v = c.Last()
			} else if binary.BigEndian.Uint64(k) > seq {
				k, v = c.Prev()
			}
		}
		for ; k != nil && found < option.MaxAnchors; k, v = c.Prev() {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, err := decodeEntry(v)
			if err != nil {
				return err
			}
			var anchor entry.HandoffAnchor
			if err := json.Unmarshal([]byte(e.GetSummary()), &anchor); err != nil {
				return fmt.Errorf("bbolt: rewind: decode anchor %d: %w", e.GetID(), err)
			}
			r := view.EntryRange{SeqS: anchor.SeqS, SeqE: anchor.SeqE}
			if found == 0 {
				result = r
			} else {
				result.SeqS = min(result.SeqS, r.SeqS)
				result.SeqE = max(result.SeqE, r.SeqE)
			}
			found++
		}
		return nil
	})
	if err != nil {
		return view.EntryRange{}, err
	}
	if found == 0 {
		return view.EntryRange{}, fmt.Errorf("bbolt: rewind: %w before seq %d", storage.ErrNoAnchor, option.FromSeq)
	}
	return result, nil
}

func (b *Bbolt) readMeta(ownerID string) (metaState, error) {
	if b.db == nil {
		return metaState{}, errors.New("bbolt: storage is not initialized")
	}
	var state metaState
	err := b.db.View(func(tx *bolt.Tx) error {
		meta, err := sessionBucket(tx, metaBucket, ownerID, b.sessionID, false)
		if err != nil || meta == nil {
			return err
		}
		state, err = decodeMeta(meta.Get(stateKey))
		return err
	})
	return state, err
}

func sessionBucket(tx *bolt.Tx, top []byte, ownerID, sessionID string, create bool) (*bolt.Bucket, error) {
	b := tx.Bucket(top)
	if b == nil && create {
		var err error
		b, err = tx.CreateBucket(top)
		if err != nil {
			return nil, err
		}
	}
	if b == nil {
		return nil, nil
	}
	ownerKey := []byte(ownerID)
	ownerBucket := b.Bucket(ownerKey)
	if ownerBucket == nil && create {
		var err error
		ownerBucket, err = b.CreateBucket(ownerKey)
		if err != nil {
			return nil, err
		}
	}
	if ownerBucket == nil {
		return nil, nil
	}
	sessionKey := []byte(sessionID)
	sessionBucket := ownerBucket.Bucket(sessionKey)
	if sessionBucket == nil && create {
		var err error
		sessionBucket, err = ownerBucket.CreateBucket(sessionKey)
		if err != nil {
			return nil, err
		}
	}
	return sessionBucket, nil
}

func seqKey(seq uint64) []byte {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	return key[:]
}

func decodeEntry(data []byte) (entry.EntryLike, error) {
	var probe struct {
		Extensions json.RawMessage
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, err
	}
	if probe.Extensions != nil {
		var e entry.CustomEntry
		if err := json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		return e, nil
	}
	var e entry.Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return e, nil
}

func decodeMeta(data []byte) (metaState, error) {
	if data == nil {
		return metaState{}, nil
	}
	var state metaState
	if err := json.Unmarshal(data, &state); err != nil {
		return metaState{}, fmt.Errorf("bbolt: decode meta: %w", err)
	}
	return state, nil
}

func putMeta(bucket *bolt.Bucket, state metaState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("bbolt: encode meta: %w", err)
	}
	return bucket.Put(stateKey, data)
}

func min(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

var _ io.Closer = (*Bbolt)(nil)
