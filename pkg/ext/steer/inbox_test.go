package steer_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/scbizu/tape-go/pkg/ext/steer"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/storage/bbolt"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const (
	testOwnerID      = "u:alice"
	mainSessionID    = "session"
	sidecarSessionID = "session:steer"
)

func TestInboxReplay(t *testing.T) {
	for _, backend := range []string{"bbolt", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			dir := t.TempDir()
			ctx := t.Context()
			tapeCtx := owner.WithOwnerId(ctx, testOwnerID)
			main := openTape(t, backend, dir, mainSessionID)
			mainEntry, err := main.Store(tapeCtx, entry.NewEntry(
				entry.WithEntryKind(entry.EntryUser),
				entry.WithEntryOwner(testOwnerID),
				entry.WithEntryContent("main input"),
			))
			if err != nil {
				t.Fatal(err)
			}
			// Use one open bbolt handle at a time for the shared database file.
			if err := main.Close(); err != nil {
				t.Fatal(err)
			}
			inbox := bindInbox(t, openTape(t, backend, dir, sidecarSessionID))
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
			if first != mainEntry.GetID() {
				t.Fatalf("sidecar first Seq = %s, want independent sequence starting at %s", first, mainEntry.GetID())
			}
			if backend == "jsonl" {
				for _, sessionID := range []string{mainSessionID, sidecarSessionID} {
					files, err := filepath.Glob(filepath.Join(dir, testOwnerID, sessionID, "*.jsonl"))
					if err != nil || len(files) != 1 {
						t.Fatalf("JSONL files for %q = %v, %v", sessionID, files, err)
					}
					info, err := os.Stat(files[0])
					if err != nil || info.Size() == 0 {
						t.Fatalf("JSONL file for %q is empty or unreadable: %v", sessionID, err)
					}
				}
			}
			if err := inbox.Tape().Close(); err != nil {
				t.Fatal(err)
			}
			reopened := bindInbox(t, openTape(t, backend, dir, sidecarSessionID))
			checkPending(t, reopened, steer.Message{ID: second, Text: "second input"})
			if err := reopened.Ack(ctx, second); err != nil {
				t.Fatal(err)
			}
			checkPending(t, reopened)
			head, err := reopened.Tape().Get(tapeCtx)
			if err != nil {
				t.Fatal(err)
			}
			overview, err := reopened.Tape().OverView(tapeCtx, view.EntryRange{SeqS: first, SeqE: head.Scope.SeqE.Next()})
			if err != nil {
				t.Fatal(err)
			}
			log := overview.Views[0]
			if log.SessionId != sidecarSessionID || log.Owner != testOwnerID {
				t.Fatalf("sidecar view session = %q, owner = %q", log.SessionId, log.Owner)
			}
			wantKinds := []entry.EntryKind{"steer:queued", "steer:queued", "steer:acked", "steer:acked", "steer:acked", "steer:acked"}
			wantTexts := []string{" first input ", "second input", first.String(), first.String(), "500", second.String()}
			if len(log.Raw) != len(wantKinds) {
				t.Fatalf("sidecar log length = %d, want %d", len(log.Raw), len(wantKinds))
			}
			for i, e := range log.Raw {
				if e.GetKind() != wantKinds[i] || e.GetSummary() != wantTexts[i] || e.GetOwner() != testOwnerID {
					t.Errorf("sidecar record %d = %+v, want kind %q, text %q, owner %q", i, e, wantKinds[i], wantTexts[i], testOwnerID)
				}
			}
			if err := reopened.Tape().Close(); err != nil {
				t.Fatal(err)
			}
			main = openTape(t, backend, dir, mainSessionID)
			entries, err := main.Range(tapeCtx, view.EntryRange{SeqS: first, SeqE: head.Scope.SeqE.Next()})
			if err != nil || len(entries.Raw) != 1 || entries.Raw[0].GetSummary() != "main input" {
				t.Fatalf("main tape records = %v, %v", entries.Raw, err)
			}
		})
	}
}

func TestInboxConcurrentEnqueuePendingAndAck(t *testing.T) {
	for _, backend := range []string{"bbolt", "jsonl"} {
		t.Run(backend, func(t *testing.T) {
			sidecar := openTape(t, backend, t.TempDir(), sidecarSessionID)
			inbox := bindInbox(t, sidecar)
			reader := bindInbox(t, sidecar)
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
	sidecar := openTape(t, "bbolt", t.TempDir(), sidecarSessionID)
	store := &rangeRecordingStorage{TapeStorage: sidecar.TapeStorage}
	sidecar.TapeStorage = store
	reader := bindInbox(t, sidecar)
	writer := bindInbox(t, sidecar)
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

func openTape(t *testing.T, backend, dir, sessionID string) *tape.Tape {
	t.Helper()
	var store storage.TapeStorage
	var err error
	if backend == "jsonl" {
		store, err = jsonl.NewJSONLStorage(sessionID, dir)
	} else {
		store, err = bbolt.NewBboltStorage(sessionID, filepath.Join(dir, "tape.db"))
	}
	if err != nil {
		t.Fatal(err)
	}
	tp := &tape.Tape{TapeStorage: store, OwnerID: testOwnerID}
	t.Cleanup(func() {
		if closer, ok := store.(*bbolt.Bbolt); ok {
			if err := closer.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if err := tp.Init(owner.WithOwnerId(t.Context(), tp.OwnerID)); err != nil {
		t.Fatal(err)
	}
	return tp
}

func bindInbox(t *testing.T, tp *tape.Tape) *steer.Inbox {
	t.Helper()
	inbox, err := steer.NewInbox(tp)
	if err != nil {
		t.Fatal(err)
	}
	if inbox.Tape() != tp {
		t.Fatal("Inbox did not retain the caller's Sidecar Tape")
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
