package tape

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
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
		if got[i].Seq != uint64(i+1) {
			t.Fatalf("entry %d seq mismatch: want %d, got %d", i, i+1, got[i].Seq)
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

func (noopStorage) Range(context.Context, view.EntryRange) (view.EntryView, error) {
	return view.EntryView{}, nil
}

func (noopStorage) Rewind(context.Context, ...storage.RewindBy) (view.EntryRange, error) {
	return view.EntryRange{}, nil
}

func (noopStorage) Search(context.Context, ...storage.SearchBy) (view.EntryView, error) {
	return view.EntryView{}, nil
}

type closableStorage struct {
	noopStorage
	closed bool
}

func (s *closableStorage) Close() error {
	s.closed = true
	return nil
}
