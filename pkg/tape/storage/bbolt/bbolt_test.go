package bbolt

import (
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

	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("hello"))); err != nil {
		t.Fatal(err)
	}
	tv, err := store.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tv.Owner != "owner-a" || tv.SessionID != "session-a" || tv.Scope.SeqE != 1 {
		t.Fatalf("Get mismatch: %+v", tv)
	}

	got, err := store.Range(ctx, view.EntryRange{SeqS: 1, SeqE: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != 1 || got.Raw[0].GetID() != 1 || got.Raw[0].GetSummary() != "hello" {
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
		got, err := store.Range(tc.ctx, view.EntryRange{SeqS: 1, SeqE: 2})
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
	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryID(7), entry.WithEntryTimestamp(time.Unix(10, 0)))); err != nil {
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
	if tv.Scope.SeqE != 7 {
		t.Fatalf("reload seq mismatch: want 7, got %d", tv.Scope.SeqE)
	}
	if err := reloaded.Store(ctx, entry.NewEntry(entry.WithEntryTimestamp(time.Unix(1, 0)))); err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Range(ctx, view.EntryRange{SeqS: 8, SeqE: 9}, storage.WithRangeAfter(time.Unix(10, 0)))
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

	got, err := store.Range(ctx, view.EntryRange{SeqS: 1, SeqE: writes + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != writes {
		t.Fatalf("entries len mismatch: want %d, got %d", writes, len(got.Raw))
	}
	for i, e := range got.Raw {
		if e.GetID() != uint64(i+1) {
			t.Fatalf("entry %d seq mismatch: got %d", i, e.GetID())
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
	payload, err := json.Marshal(entry.HandoffAnchor{SeqS: 1, SeqE: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctx, entry.NewAnchor(0, "owner-a", entry.AnchorKindHandoff, payload)); err != nil {
		t.Fatal(err)
	}

	got, err := store.Rewind(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != (view.EntryRange{SeqS: 1, SeqE: 3}) {
		t.Fatalf("rewind mismatch: %+v", got)
	}
	if _, err := store.Rewind(ctx, storage.WithRewindFromSeq(1)); !errors.Is(err, storage.ErrNoAnchor) {
		t.Fatalf("rewind before first anchor: want ErrNoAnchor, got %v", err)
	}
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
