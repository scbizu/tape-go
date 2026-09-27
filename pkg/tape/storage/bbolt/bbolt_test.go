package bbolt

import (
	"bytes"
	"context"
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
)

func TestBboltStoreGetRange(t *testing.T) {
	t.Parallel()

	store, ctx := newStore(t, "owner-a", "session-a")
	defer store.Close()

	stored, err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("hello")))
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetID() != seq(1) {
		t.Fatalf("stored entry ID = %s, want 1", stored.GetID())
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

func TestBboltEnumeratesPersistedAnchors(t *testing.T) {
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
	for _, kind := range []entry.AnchorKind{entry.AnchorKindHandoff, entry.AnchorKindCustom} {
		if _, err := store.Store(ctx, entry.NewAnchor(entry.Seq{}, "owner-a", kind, json.RawMessage(`{}`))); err != nil {
			t.Fatal(err)
		}
	}
	check := func(s *Bbolt) {
		t.Helper()
		var ids []entry.Seq
		for anchor, err := range s.Anchors(ctx) {
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, anchor.GetID())
		}
		if len(ids) != 2 || ids[0] != seq(1) || ids[1] != seq(2) {
			t.Fatalf("anchors = %v", ids)
		}
	}
	check(store)
	otherOwner := owner.WithOwnerId(context.Background(), "owner-b")
	if err := store.Init(otherOwner); err != nil {
		t.Fatal(err)
	}
	for anchor, err := range store.Anchors(otherOwner) {
		t.Fatalf("other owner anchor = %v, %v", anchor, err)
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
	check(reloaded)
	if err := reloaded.Close(); err != nil {
		t.Fatal(err)
	}
	otherSession, err := NewBboltStorage("session-b", path)
	if err != nil {
		t.Fatal(err)
	}
	defer otherSession.Close()
	if err := otherSession.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for anchor, err := range otherSession.Anchors(ctx) {
		t.Fatalf("other session anchor = %v, %v", anchor, err)
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
	if _, err := store.Store(ctxA, entry.NewEntry(entry.WithEntryOwner("owner-a"))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store(ctxB, entry.NewEntry(entry.WithEntryOwner("owner-b"))); err != nil {
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
	if _, err := store.Store(ctx, entry.NewEntry(entry.WithEntryID(seq(7)), entry.WithEntryTimestamp(time.Unix(10, 0)))); err != nil {
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
	if _, err := reloaded.Store(ctx, entry.NewEntry(entry.WithEntryTimestamp(time.Unix(1, 0)))); err != nil {
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
			if _, err := store.Store(ctx, entry.Entry{}); err != nil {
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
		if _, err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent(text))); err != nil {
			t.Fatal(err)
		}
	}
	payload, err := json.Marshal(entry.HandoffAnchor{SeqS: seq(1), SeqE: seq(3)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Store(ctx, entry.NewAnchor(entry.Seq{}, "owner-a", entry.AnchorKindHandoff, payload)); err != nil {
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
		if _, err := store.Store(ctx, entry.NewEntry(entry.WithEntryID(id))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Store(ctx, entry.NewEntry()); err != nil {
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
