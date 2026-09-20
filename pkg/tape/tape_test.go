package tape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/finder"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
)

func TestTapeWriteAndReadEntriesThroughSystemIO(t *testing.T) {
	t.Parallel()

	tape := newMemoryTape(t, "owner-a", "session-a")
	payloads := [][]byte{
		[]byte(`"hello"`),
		[]byte(`"world"`),
	}

	for _, payload := range payloads {
		n, err := tape.Write(payload)
		if err != nil {
			t.Fatalf("Write(%s): %v", payload, err)
		}
		if n != len(payload) {
			t.Fatalf("Write(%s) bytes mismatch: want %d, got %d", payload, len(payload), n)
		}
	}

	data := readAllWithSmallBuffer(t, tape, 7)
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != len(payloads) {
		t.Fatalf("JSONL lines mismatch: want %d, got %d from %s", len(payloads), len(lines), data)
	}
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatalf("invalid JSONL line: %s", line)
		}
	}
	got := decodeEntryViews(t, data)

	wantText := []string{"hello", "world"}
	if len(got) != len(wantText) {
		t.Fatalf("entries len mismatch: want %d, got %d from %s", len(wantText), len(got), data)
	}
	for i, want := range wantText {
		if got[i].Seq != entry.SeqFromUint64(uint64(i+1)) {
			t.Fatalf("entry %d seq mismatch: want %d, got %s", i, i+1, got[i].Seq)
		}
		if got[i].Owner != "owner-a" {
			t.Fatalf("entry %d owner mismatch: want %q, got %q", i, "owner-a", got[i].Owner)
		}
		if got[i].Text != want {
			t.Fatalf("entry %d text mismatch: want %q, got %q", i, want, got[i].Text)
		}
	}

	buf := make([]byte, 1)
	n, err := tape.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read after EOF mismatch: want 0, EOF; got %d, %v", n, err)
	}
}

func TestTapeCloseDelegatesToUnderlyingCloser(t *testing.T) {
	t.Parallel()

	closer := &closableStorage{}
	tape := &Tape{TapeStorage: closer}

	if err := tape.Close(); err != nil {
		t.Fatalf("Close with closer storage: %v", err)
	}
	if !closer.closed {
		t.Fatal("Close did not delegate to underlying storage")
	}

	tape = &Tape{TapeStorage: noopStorage{}}
	if err := tape.Close(); err != nil {
		t.Fatalf("Close with non-closer storage: %v", err)
	}
}

type anchorMakerFunc func(context.Context, entry.EntryLike, view.EntryView) (entry.EntryLike, bool, error)

func (f anchorMakerFunc) MakeAnchor(ctx context.Context, e entry.EntryLike, memory view.EntryView) (entry.EntryLike, bool, error) {
	return f(ctx, e, memory)
}

func TestTapeSetViewPreservesAnchorMaker(t *testing.T) {
	t.Parallel()

	maker := anchorMakerFunc(func(context.Context, entry.EntryLike, view.EntryView) (entry.EntryLike, bool, error) {
		return nil, false, nil
	})
	tape := &Tape{View: view.EntryView{AnchorMaker: maker}}
	tape.SetView(view.EntryRange{SeqS: entry.SeqFromUint64(3)})

	if tape.View.AnchorMaker == nil {
		t.Fatal("SetView cleared the view's AnchorMaker")
	}
	if tape.View.Scope.SeqS != entry.SeqFromUint64(3) {
		t.Fatalf("view start = %s, want 3", tape.View.Scope.SeqS)
	}
}

func TestTapeStoreUsesStorageWithoutRunningAnchorMaker(t *testing.T) {
	t.Parallel()

	tape := newMemoryTape(t, "owner-a", "session-a")
	called := false
	tape.View.AnchorMaker = anchorMakerFunc(func(context.Context, entry.EntryLike, view.EntryView) (entry.EntryLike, bool, error) {
		called = true
		return nil, false, nil
	})
	ctx := owner.WithOwnerId(context.Background(), "owner-a")

	if err := tape.Store(ctx, entry.NewEntry(entry.WithEntryContent("raw store"))); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("storage Store invoked the view's AnchorMaker")
	}
}

func TestTapeJevAnchoringRunsAfterPrimaryStore(t *testing.T) {
	t.Parallel()

	tape := newMemoryTape(t, "owner-a", "session-a")
	tape.View.AnchorMaker = anchorMakerFunc(func(_ context.Context, e entry.EntryLike, memory view.EntryView) (entry.EntryLike, bool, error) {
		if len(memory.Raw) != 1 || memory.Raw[0].GetSummary() != "durable" {
			t.Fatalf("primary entry was not stored before Jev decision: %#v", memory.Raw)
		}
		payload, _ := json.Marshal(entry.JevAnchor{
			State: json.RawMessage(`{"overview":"durable"}`),
			SeqS:  memory.Scope.SeqS,
			SeqE:  memory.Scope.SeqE,
		})
		return entry.NewAnchor(entry.Seq{}, e.GetOwner(), entry.AnchorKindJev, payload), true, nil
	})
	base := tape.TapeStorage
	tape.TapeStorage = storage.NewAnchoringStorage(base, &tape.View, nil)
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := tape.Store(ctx, entry.NewEntry(entry.WithEntryContent("durable"), entry.WithEntryOwner("owner-a"))); err != nil {
		t.Fatal(err)
	}
	index := base.(interface {
		CandidateIndex(context.Context) ([]finder.Candidate, error)
	})
	candidates, err := index.CandidateIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Scope != (view.EntryRange{
		SeqS: entry.SeqFromUint64(1),
		SeqE: entry.SeqFromUint64(2),
	}) {
		t.Fatalf("candidates = %#v", candidates)
	}
	if _, err := tape.Rewind(ctx); !errors.Is(err, storage.ErrNoAnchor) {
		t.Fatalf("full rewind recognized Jev anchor: %v", err)
	}
}

func TestTapeJevAnchorFailureIsFailOpen(t *testing.T) {
	t.Parallel()

	tape := newMemoryTape(t, "owner-a", "session-a")
	want := errors.New("classifier unavailable")
	tape.View.AnchorMaker = anchorMakerFunc(func(context.Context, entry.EntryLike, view.EntryView) (entry.EntryLike, bool, error) {
		return nil, false, want
	})
	var reported error
	tape.TapeStorage = storage.NewAnchoringStorage(
		tape.TapeStorage,
		&tape.View,
		func(err error) { reported = err },
	)
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := tape.Store(ctx, entry.NewEntry(entry.WithEntryContent("committed"))); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(reported, want) {
		t.Fatalf("reported error = %v", reported)
	}
	got, err := tape.Range(ctx, view.EntryRange{
		SeqS: entry.SeqFromUint64(1),
		SeqE: entry.SeqFromUint64(2),
	})
	if err != nil || len(got.Raw) != 1 || got.Raw[0].GetSummary() != "committed" {
		t.Fatalf("primary entry was not committed: %#v, %v", got, err)
	}
}

func newMemoryTape(t *testing.T, ownerID, sessionID string) *Tape {
	t.Helper()

	store, err := jsonl.NewJSONLStorage(sessionID, "/tapes")
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}
	store.Fs = afero.NewMemMapFs()

	ctx := owner.WithOwnerId(context.Background(), ownerID)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	return &Tape{
		TapeStorage: store,
		OwnerID:     ownerID,
	}
}

func readAllWithSmallBuffer(t *testing.T, r io.Reader, size int) []byte {
	t.Helper()

	var out bytes.Buffer
	buf := make([]byte, size)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if errors.Is(err, io.EOF) {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
}

func decodeEntryViews(t *testing.T, data []byte) []entry.Entry {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))
	var entries []entry.Entry
	for {
		var e entry.Entry
		if err := decoder.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				return entries
			}
			t.Fatalf("decode entry from %s: %v", data, err)
		}
		entries = append(entries, e)
	}
}

type noopStorage struct{}

func (noopStorage) Init(context.Context) error {
	return nil
}

func (noopStorage) Get(context.Context) (view.TapeView, error) {
	return view.TapeView{}, nil
}

func (noopStorage) Store(context.Context, entry.EntryLike) error {
	return nil
}

func (noopStorage) Range(context.Context, view.EntryRange, ...storage.RangeBy) (view.EntryView, error) {
	return view.EntryView{}, nil
}

func (noopStorage) Rewind(context.Context, ...storage.RewindBy) (view.EntryRange, error) {
	return view.EntryRange{}, nil
}

type closableStorage struct {
	noopStorage
	closed bool
}

func (s *closableStorage) Close() error {
	s.closed = true
	return nil
}
