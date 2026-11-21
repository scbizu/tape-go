package steer_test

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/scbizu/tape-go/pkg/ext/steer"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/storage/bbolt"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

func TestInboxReplay(t *testing.T) {
	for _, backend := range []string{"bbolt", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			ctx := t.Context()
			store := openTape(t, backend, dir)
			inbox := bindInbox(t, store, "inbox")
			mainCtx := owner.WithOwnerId(ctx, "main")
			if err := store.Init(mainCtx); err != nil {
				t.Fatal(err)
			}
			checkPending(t, inbox)
			first, err := inbox.Enqueue(ctx, " first input ")
			if err != nil {
				t.Fatal(err)
			}
			second, err := inbox.Enqueue(ctx, "second input")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := inbox.Enqueue(ctx, "  "); err == nil {
				t.Fatal("Enqueue accepted blank input")
			}
			checkPending(t, inbox, steer.Message{ID: first, Text: " first input "}, steer.Message{ID: second, Text: "second input"})
			for range 2 {
				if err := inbox.Ack(ctx, first); err != nil {
					t.Fatal(err)
				}
			}
			if err := inbox.Ack(ctx, entry.Seq{}); err == nil {
				t.Fatal("Ack accepted a zero ID")
			}
			if err := inbox.Ack(ctx, entry.SeqFromUint64(500)); err != nil {
				t.Fatalf("Ack of an unknown nonzero ID: %v", err)
			}
			if head, err := store.Get(mainCtx); err != nil || !head.Scope.SeqE.IsZero() {
				t.Fatalf("main tape head = %v, err = %v", head, err)
			}

			if closer, ok := store.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			reopened := bindInbox(t, openTape(t, backend, dir), "inbox")
			checkPending(t, reopened, steer.Message{ID: second, Text: "second input"})
			if err := reopened.Ack(ctx, second); err != nil {
				t.Fatal(err)
			}
			checkPending(t, reopened)
		})
	}
}

func TestInboxConcurrentEnqueuePendingAndAck(t *testing.T) {
	for _, backend := range []string{"bbolt", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			store := openTape(t, backend, t.TempDir())
			inbox := bindInbox(t, store, "inbox")
			reader, err := steer.NewInbox(store, "inbox")
			if err != nil {
				t.Fatal(err)
			}
			const count = 24
			var wg sync.WaitGroup
			unacked := make([]steer.Message, count/2)
			for n := range count {
				wg.Go(func() {
					ctx := t.Context()
					text := fmt.Sprintf("message %d", n)
					id, err := inbox.Enqueue(ctx, text)
					if err != nil {
						t.Error(err)
						return
					}
					if _, err := reader.Pending(ctx); err != nil {
						t.Error(err)
					}
					if n%2 == 0 {
						if err := reader.Ack(ctx, id); err != nil {
							t.Error(err)
						}
						return
					}
					unacked[n/2] = steer.Message{ID: id, Text: text}
				})
			}
			wg.Wait()
			slices.SortFunc(unacked, func(a, b steer.Message) int { return a.ID.Cmp(b.ID) })
			checkPending(t, inbox, unacked...)
		})
	}
}

func TestInboxProjectsOnlyNewTail(t *testing.T) {
	ctx := t.Context()
	store := &rangeRecordingStorage{TapeStorage: openTape(t, "bbolt", t.TempDir())}
	reader := bindInbox(t, store, "inbox")
	writer, err := steer.NewInbox(store, "inbox")
	if err != nil {
		t.Fatal(err)
	}
	checkPending(t, reader)
	if len(store.ranges) != 0 {
		t.Fatalf("ranges for empty inbox = %v", store.ranges)
	}

	// Acknowledging an ID before it is enqueued leaves the later message pending
	// on both incremental reads and full replay.
	if err := writer.Ack(ctx, entry.SeqFromUint64(2)); err != nil {
		t.Fatal(err)
	}
	checkPending(t, reader)
	id, err := writer.Enqueue(ctx, "message")
	if err != nil {
		t.Fatal(err)
	}
	want := steer.Message{ID: id, Text: "message"}
	pending, err := reader.Pending(ctx)
	if err != nil || !slices.Equal(pending, []steer.Message{want}) {
		t.Fatalf("Pending = %v, %v, want %v", pending, err, want)
	}
	pending[0].Text = "changed by caller"
	checkPending(t, reader, want)
	checkPending(t, writer, want)
	if err := writer.Ack(ctx, id); err != nil {
		t.Fatal(err)
	}
	wantRanges := []view.EntryRange{
		{SeqS: entry.SeqFromUint64(1), SeqE: id},
		{SeqS: id, SeqE: id.Next()},
		{SeqS: entry.SeqFromUint64(1), SeqE: id.Next()},
	}
	if !slices.Equal(store.ranges, wantRanges) {
		t.Fatalf("ranges before projecting Ack = %v, want %v", store.ranges, wantRanges)
	}
	checkPending(t, writer)
	checkPending(t, reader)
	wantRanges = append(wantRanges,
		view.EntryRange{SeqS: id.Next(), SeqE: id.Next().Next()},
		view.EntryRange{SeqS: id.Next(), SeqE: id.Next().Next()},
	)
	if !slices.Equal(store.ranges, wantRanges) {
		t.Fatalf("ranges = %v, want %v", store.ranges, wantRanges)
	}
}

type rangeRecordingStorage struct {
	storage.TapeStorage
	ranges []view.EntryRange
}

func (s *rangeRecordingStorage) Range(ctx context.Context, scope view.EntryRange, opts ...storage.RangeBy) (view.EntryView, error) {
	s.ranges = append(s.ranges, scope)
	return s.TapeStorage.Range(ctx, scope, opts...)
}

func openTape(t *testing.T, backend, dir string) storage.TapeStorage {
	t.Helper()
	if backend == "jsonl" {
		store, err := jsonl.NewJSONLStorage("session", dir)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	store, err := bbolt.NewBboltStorage("session", filepath.Join(dir, "tape.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func bindInbox(t *testing.T, store storage.TapeStorage, ownerID string) *steer.Inbox {
	t.Helper()
	if err := store.Init(owner.WithOwnerId(t.Context(), ownerID)); err != nil {
		t.Fatal(err)
	}
	inbox, err := steer.NewInbox(store, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	return inbox
}

func checkPending(t *testing.T, inbox *steer.Inbox, want ...steer.Message) {
	t.Helper()
	got, err := inbox.Pending(t.Context())
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Pending = %v, %v, want %v", got, err, want)
	}
}
