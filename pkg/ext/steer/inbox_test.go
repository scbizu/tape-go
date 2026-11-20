package steer_test

import (
	"errors"
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
			for _, id := range []entry.Seq{{}, second.Next(), entry.SeqFromUint64(500)} {
				if err := inbox.Ack(ctx, id); !errors.Is(err, steer.ErrUnknownID) {
					t.Fatalf("Ack(%s) = %v, want ErrUnknownID", id, err)
				}
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
