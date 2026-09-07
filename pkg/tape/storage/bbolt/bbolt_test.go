package bbolt

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
	bolt "go.etcd.io/bbolt"
)

func TestBboltStoreGetRange(t *testing.T) {
	t.Parallel()

	store, ctx := newStore(t, "owner-a", "session-a")
	defer store.Close()

	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("hello"))); err != nil {
		t.Fatal(err)
	}
	tv, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tv.Owner != "owner-a" || tv.SessionID != "session-a" || tv.Scope.SeqE != seq(1) {
		t.Fatalf("Get mismatch: %+v", tv)
	}

	got, err := store.Range(ctx, view.EntryRange{SeqS: seq(1), SeqE: seq(2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != 1 || got.Raw[0].GetID() != seq(1) || got.Raw[0].GetSummary() != "hello" {
		t.Fatalf("Range mismatch: %#v", got.Raw)
	}
}

func TestBboltSeparatesOwnerState(t *testing.T) {
	t.Parallel()

	store, err := NewBboltStorage("session-a", filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctxA := owner.WithOwnerId(context.Background(), "owner-a")
	ctxB := owner.WithOwnerId(context.Background(), "owner-b")
	for _, ctx := range []context.Context{ctxA, ctxB} {
		if err := store.Init(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Store(ctxA, entry.NewEntry(entry.WithEntryOwner("owner-a"))); err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctxB, entry.NewEntry(entry.WithEntryOwner("owner-b"))); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		ctx     context.Context
		ownerID string
	}{
		{ctxA, "owner-a"},
		{ctxB, "owner-b"},
	} {
		got, err := store.Range(tc.ctx, view.EntryRange{SeqS: seq(1), SeqE: seq(2)})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Raw) != 1 || got.Raw[0].GetOwner() != tc.ownerID {
			t.Fatalf("owner %s range mismatch: %#v", tc.ownerID, got.Raw)
		}
	}
}

func TestBboltReloadRestoresMeta(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tape.db")
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	store, err := NewBboltStorage("session-a", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryID(seq(7)), entry.WithEntryTimestamp(time.Unix(10, 0)))); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := NewBboltStorage("session-a", path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if err := reloaded.Init(ctx); err != nil {
		t.Fatal(err)
	}
	tv, err := reloaded.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tv.Scope.SeqE != seq(7) {
		t.Fatalf("reload seq mismatch: want 7, got %s", tv.Scope.SeqE)
	}
	if err := reloaded.Store(ctx, entry.NewEntry(entry.WithEntryTimestamp(time.Unix(1, 0)))); err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Range(ctx, view.EntryRange{SeqS: seq(8), SeqE: seq(9)}, storage.WithRangeAfter(time.Unix(10, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != 1 || !got.Raw[0].GetTimestamp().After(time.Unix(10, 0)) {
		t.Fatalf("timestamp did not grow after reload: %#v", got.Raw)
	}
}

func TestBboltAssignsEntryIDsAtomically(t *testing.T) {
	t.Parallel()

	store, ctx := newStore(t, "owner-a", "session-a")
	defer store.Close()

	const writes = 20
	var wg sync.WaitGroup
	for range writes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.Store(ctx, entry.Entry{}); err != nil {
				t.Errorf("Store: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := store.Range(ctx, view.EntryRange{SeqS: seq(1), SeqE: seq(writes + 1)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != writes {
		t.Fatalf("entries len mismatch: want %d, got %d", writes, len(got.Raw))
	}
	for i, e := range got.Raw {
		if e.GetID() != seq(i+1) {
			t.Fatalf("entry %d seq mismatch: got %s", i, e.GetID())
		}
	}
}

func TestBboltRewind(t *testing.T) {
	t.Parallel()

	store, ctx := newStore(t, "owner-a", "session-a")
	defer store.Close()

	for _, text := range []string{"one", "two"} {
		if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent(text))); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(entry.HandoffAnchor{SeqS: seq(1), SeqE: seq(3)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctx, entry.NewAnchor(entry.Seq{}, "owner-a", entry.AnchorKindHandoff, payload)); err != nil {
		t.Fatal(err)
	}

	got, err := store.Rewind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != (view.EntryRange{SeqS: seq(1), SeqE: seq(3)}) {
		t.Fatalf("rewind mismatch: %+v", got)
	}
	if _, err := store.Rewind(ctx, storage.WithRewindFromSeq(seq(1))); !errors.Is(err, storage.ErrNoAnchor) {
		t.Fatalf("rewind before first anchor: want ErrNoAnchor, got %v", err)
	}
}

func TestBboltOrdersSequencesBeyondUint64(t *testing.T) {
	t.Parallel()

	store, ctx := newStore(t, "owner-a", "session-a")
	defer store.Close()
	first := entry.MustParseSeq("18446744073709551616")
	second := entry.MustParseSeq("18446744073709551617")
	for _, id := range []entry.Seq{second, first} {
		if err := store.Store(ctx, entry.NewEntry(entry.WithEntryID(id))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Store(ctx, entry.NewEntry()); err != nil {
		t.Fatal(err)
	}
	third := second.Next()
	got, err := store.Range(ctx, view.EntryRange{SeqS: first, SeqE: third.Next()})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != 3 || got.Raw[0].GetID() != first || got.Raw[1].GetID() != second || got.Raw[2].GetID() != third {
		t.Fatalf("range order = %#v", got.Raw)
	}
}

func TestSeqKeyOrderMatchesNumericOrder(t *testing.T) {
	values := []entry.Seq{
		entry.SeqFromUint64(1),
		entry.SeqFromUint64(255),
		entry.SeqFromUint64(256),
		entry.SeqFromUint64(^uint64(0)),
		entry.MustParseSeq("18446744073709551616"),
		entry.MustParseSeq("9999999999999999999999999999999999999999"),
	}
	for i, value := range values {
		decoded, err := decodeSeqKey(seqKey(value))
		if err != nil || decoded != value {
			t.Fatalf("key round trip %s = %s, %v", value, decoded, err)
		}
		if i > 0 && bytes.Compare(seqKey(values[i-1]), seqKey(value)) >= 0 {
			t.Fatalf("key order %s >= %s", values[i-1], value)
		}
	}
}

func TestBboltInitMigratesLegacyUint64Keys(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, top := range [][]byte{entriesBucket, anchorsBucket, metaBucket} {
			if _, err := tx.CreateBucketIfNotExists(top); err != nil {
				return err
			}
		}
		entries, err := sessionBucket(tx, entriesBucket, "owner-a", "session-a", true)
		if err != nil {
			return err
		}
		meta, err := sessionBucket(tx, metaBucket, "owner-a", "session-a", true)
		if err != nil {
			return err
		}
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], 7)
		if err := entries.Put(key[:], []byte(`{"Seq":7,"Ek":"user","Text":"legacy","Owner":"owner-a","Timestamp":"2026-09-07T00:00:00Z"}`)); err != nil {
			return err
		}
		anchors, err := sessionBucket(tx, anchorsBucket, "owner-a", "session-a", true)
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint64(key[:], 8)
		anchor := []byte(`{"Seq":8,"Ek":"anchor:handoff","Text":"{\"Summary\":\"legacy anchor\",\"SeqS\":7,\"SeqE\":9}","Owner":"owner-a","Timestamp":"2026-09-07T00:00:01Z"}`)
		if err := entries.Put(key[:], anchor); err != nil {
			return err
		}
		if err := anchors.Put(key[:], anchor); err != nil {
			return err
		}
		return meta.Put(stateKey, []byte(`{"LastSeq":8,"LastTimestamp":"2026-09-07T00:00:01Z"}`))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := NewBboltStorage("session-a", path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := store.Range(ctx, view.EntryRange{SeqS: seq(7), SeqE: seq(9)})
	if err != nil || len(got.Raw) != 2 || got.Raw[0].GetSummary() != "legacy" {
		t.Fatalf("legacy range = %#v, %v", got.Raw, err)
	}
	rewound, err := store.Rewind(ctx)
	if err != nil || rewound != (view.EntryRange{SeqS: seq(7), SeqE: seq(9)}) {
		t.Fatalf("legacy rewind = %+v, %v", rewound, err)
	}
	err = store.db.View(func(tx *bolt.Tx) error {
		entries, err := sessionBucket(tx, entriesBucket, "owner-a", "session-a", false)
		if err != nil {
			return err
		}
		key, _ := entries.Cursor().First()
		if len(key) == 8 {
			return errors.New("legacy key was not migrated")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seq(value int) entry.Seq {
	return entry.SeqFromUint64(uint64(value))
}

func newStore(t *testing.T, ownerID, sessionID string) (*Bbolt, context.Context) {
	t.Helper()

	store, err := NewBboltStorage(sessionID, filepath.Join(t.TempDir(), "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := owner.WithOwnerId(context.Background(), ownerID)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}
