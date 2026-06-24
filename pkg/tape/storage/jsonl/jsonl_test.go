package jsonl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
)

func TestReadLinesKeepsJSONLBoundaries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "entries.jsonl")
	content := []byte("{\"seq\":0}\n{\"seq\":1}\n{\"seq\":2}\n")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	data, err := readLines(context.Background(), afero.NewOsFs(), file, 1, int64(LINE_EOF))
	if err != nil {
		t.Fatalf("readLines to EOF: %v", err)
	}

	want := "{\"seq\":1}\n{\"seq\":2}\n"
	if string(data) != want {
		t.Fatalf("readLines mismatch:\nwant %q\ngot  %q", want, string(data))
	}
}

func TestBuildJSONLIndex(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "entries.jsonl")
	content := []byte("{\"Seq\":7}\n{\"Seq\":9}\n{\"Seq\":13}\n")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	index, err := buildJSONLIndex(afero.NewOsFs(), file)
	if err != nil {
		t.Fatalf("buildJSONLIndex: %v", err)
	}
	if index.Path != file {
		t.Fatalf("path mismatch: want %q, got %q", file, index.Path)
	}
	if index.Scope.SeqS != 7 || index.Scope.SeqE != 13 {
		t.Fatalf("scope mismatch: want [7,13], got [%d,%d]", index.Scope.SeqS, index.Scope.SeqE)
	}
	if index.Entries != 3 {
		t.Fatalf("entries mismatch: want 3, got %d", index.Entries)
	}
}

func TestJSONLInitCreatesSessionFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewJSONLStorage("session-a", dir)
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}
	ctx := owner.WithOwnerId(context.Background(), "owner-a")

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	state := mustOwnerState(t, store, "owner-a")
	if len(state.indexes) != 1 {
		t.Fatalf("indexes len mismatch: want 1, got %d", len(state.indexes))
	}

	gotFile := state.indexes[0].Path
	if _, err := os.Stat(gotFile); err != nil {
		t.Fatalf("stat created file: %v", err)
	}

	wantDir := filepath.Join(dir, "owner-a", "session-a")
	if _, err := os.Stat(wantDir); err != nil {
		t.Fatalf("stat session dir: %v", err)
	}

	wantBase := time.Now().Format("200612") + "_0.jsonl"
	gotBase := filepath.Base(gotFile)
	if gotBase != wantBase {
		t.Fatalf("created file mismatch: want %q, got %q", wantBase, gotBase)
	}
}

func TestJSONLInitIsIdempotentForSameInstance(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewJSONLStorage("session-b", dir)
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}
	ctx := owner.WithOwnerId(context.Background(), "owner-a")

	if err := store.Init(ctx); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	state := mustOwnerState(t, store, "owner-a")
	if len(state.indexes) != 1 {
		t.Fatalf("indexes len mismatch after repeated Init: want 1, got %d", len(state.indexes))
	}
}

func TestJSONLUsesEmbeddedAferoFS(t *testing.T) {
	t.Parallel()

	store, err := NewJSONLStorage("session-memory", "/tapes")
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	want := entry.Entry{}
	if err := store.Store(ctx, want); err != nil {
		t.Fatalf("Store: %v", err)
	}

	state := mustOwnerState(t, store, "owner-a")
	data, err := afero.ReadFile(store.Fs, state.indexes[0].Path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var got entry.Entry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode stored entry: %v", err)
	}
}

func TestJSONLRoundTripsCustomEntry(t *testing.T) {
	t.Parallel()

	store, err := NewJSONLStorage("session-memory", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

	want := entry.CustomEntry{
		Entry:      entry.NewEntry(entry.WithEntryContent("hello")),
		Extensions: map[string]any{"event_id": "event-1"},
	}
	if err := store.Store(ctx, want); err != nil {
		t.Fatal(err)
	}
	view, err := store.Range(ctx, view.EntryRange{SeqS: 1, SeqE: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := view.Raw[0].(entry.CustomEntry)
	if !ok {
		t.Fatalf("entry type mismatch: %T", view.Raw[0])
	}
	if got.GetID() != 1 || got.Extensions["event_id"] != "event-1" {
		t.Fatalf("custom entry mismatch: %+v", got)
	}
	if !got.GetTimestamp().Equal(want.GetTimestamp()) {
		t.Fatalf("timestamp mismatch: want %v, got %v", want.GetTimestamp(), got.GetTimestamp())
	}
}

func TestJSONLSeparatesOwnerState(t *testing.T) {
	t.Parallel()

	store, err := NewJSONLStorage("session-shared", "/tapes")
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}
	store.Fs = afero.NewMemMapFs()

	ctxA := owner.WithOwnerId(context.Background(), "owner-a")
	ctxB := owner.WithOwnerId(context.Background(), "owner-b")
	for _, ctx := range []context.Context{ctxA, ctxB} {
		if err := store.Init(ctx); err != nil {
			t.Fatalf("Init: %v", err)
		}
	}

	if err := store.Store(ctxA, entry.NewEntry(entry.WithEntryOwner("owner-a"))); err != nil {
		t.Fatalf("Store owner-a: %v", err)
	}
	if err := store.Store(ctxB, entry.NewEntry(entry.WithEntryOwner("owner-b"))); err != nil {
		t.Fatalf("Store owner-b: %v", err)
	}

	for _, tc := range []struct {
		ctx     context.Context
		ownerID string
	}{
		{ctx: ctxA, ownerID: "owner-a"},
		{ctx: ctxB, ownerID: "owner-b"},
	} {
		state := mustOwnerState(t, store, tc.ownerID)
		data, err := afero.ReadFile(store.Fs, state.indexes[0].Path)
		if err != nil {
			t.Fatalf("Read %s: %v", tc.ownerID, err)
		}
		var got entry.Entry
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decode %s entry: %v", tc.ownerID, err)
		}
		if got.Owner != tc.ownerID {
			t.Fatalf("owner mismatch: want %q, got %q", tc.ownerID, got.Owner)
		}
	}

	stateA := mustOwnerState(t, store, "owner-a")
	stateB := mustOwnerState(t, store, "owner-b")
	if stateA.indexes[0].Path == stateB.indexes[0].Path {
		t.Fatalf("owners share the same JSONL file: %q", stateA.indexes[0].Path)
	}
}

func TestJSONLGetReturnsLastEntryID(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	store, err := NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}
	store.Fs = fs

	if err := store.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, id := range []uint64{7, 13} {
		if err := store.Store(ctx, entry.NewEntry(entry.WithEntryID(id))); err != nil {
			t.Fatalf("Store entry %d: %v", id, err)
		}
	}

	got, err := store.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Scope.SeqE != 13 {
		t.Fatalf("scope end mismatch: want 13, got %d", got.Scope.SeqE)
	}
	state := mustOwnerState(t, store, "owner-a")
	if len(state.indexes) != 1 {
		t.Fatalf("indexes len mismatch: want 1, got %d", len(state.indexes))
	}
	index := state.indexes[0]
	if index.Scope.SeqS != 7 || index.Scope.SeqE != 13 {
		t.Fatalf("index scope mismatch: want [7,13], got [%d,%d]", index.Scope.SeqS, index.Scope.SeqE)
	}
	if index.Entries != 2 {
		t.Fatalf("index entries mismatch: want 2, got %d", index.Entries)
	}

	reloaded, err := NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatalf("NewJSONLStorage reload: %v", err)
	}
	reloaded.Fs = fs
	if err := reloaded.Init(ctx); err != nil {
		t.Fatalf("reload Init: %v", err)
	}

	got, err = reloaded.Get(ctx)
	if err != nil {
		t.Fatalf("reload Get: %v", err)
	}
	if got.Scope.SeqE != 13 {
		t.Fatalf("reloaded scope end mismatch: want 13, got %d", got.Scope.SeqE)
	}
	lastTimestamp := mustOwnerState(t, reloaded, "owner-a").lastTimestamp
	if err := reloaded.Store(ctx, entry.NewEntry(entry.WithEntryTimestamp(time.Unix(1, 0)))); err != nil {
		t.Fatalf("Store after reload: %v", err)
	}
	entries, err := reloaded.Range(ctx, view.EntryRange{SeqS: 14, SeqE: 15})
	if err != nil {
		t.Fatalf("Range after reload: %v", err)
	}
	if len(entries.Raw) != 1 || !entries.Raw[0].GetTimestamp().After(lastTimestamp) {
		t.Fatalf("timestamp did not grow after reload: previous=%v entries=%v", lastTimestamp, entries.Raw)
	}
	entries, err = reloaded.Range(ctx, view.EntryRange{SeqS: 7, SeqE: 15}, storage.WithRangeAfter(lastTimestamp))
	if err != nil {
		t.Fatalf("Range after timestamp: %v", err)
	}
	if len(entries.Raw) != 2 {
		t.Fatalf("Range after timestamp returned %d entries, want 2", len(entries.Raw))
	}
}

func TestJSONLAssignsEntryIDsAtomically(t *testing.T) {
	t.Parallel()

	store, err := NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}

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
			t.Fatalf("entry %d seq mismatch: want %d, got %d", i, i+1, e.GetID())
		}
		if e.GetTimestamp().IsZero() {
			t.Fatalf("entry %d timestamp is zero", i)
		}
		if i > 0 && !e.GetTimestamp().After(got.Raw[i-1].GetTimestamp()) {
			t.Fatalf("entry %d timestamp %v does not follow %v", i, e.GetTimestamp(), got.Raw[i-1].GetTimestamp())
		}
	}
}

func TestJSONLRangeAcrossSparseIndexes(t *testing.T) {
	t.Parallel()

	fs := afero.NewMemMapFs()
	files := []struct {
		path    string
		entries []entry.Entry
	}{
		{
			path: "/tapes/owner-a/session-a/0.jsonl",
			entries: []entry.Entry{
				entry.NewEntry(entry.WithEntryID(1)),
				entry.NewEntry(entry.WithEntryID(2)),
			},
		},
		{
			path: "/tapes/owner-a/session-a/1.jsonl",
			entries: []entry.Entry{
				entry.NewEntry(entry.WithEntryID(3)),
				entry.NewEntry(entry.WithEntryID(5)),
			},
		},
		{
			path: "/tapes/owner-a/session-a/2.jsonl",
			entries: []entry.Entry{
				entry.NewEntry(entry.WithEntryID(7)),
				entry.NewEntry(entry.WithEntryID(8)),
				entry.NewEntry(entry.WithEntryID(10)),
			},
		},
	}

	indexes := make([]JSONLIndex, 0, len(files))
	for _, file := range files {
		if err := fs.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		fd, err := fs.Create(file.path)
		if err != nil {
			t.Fatalf("Create %s: %v", file.path, err)
		}
		encoder := json.NewEncoder(fd)
		for _, e := range file.entries {
			if err := encoder.Encode(e); err != nil {
				fd.Close()
				t.Fatalf("Encode %s: %v", file.path, err)
			}
		}
		if err := fd.Close(); err != nil {
			t.Fatalf("Close %s: %v", file.path, err)
		}
		index, err := buildJSONLIndex(fs, file.path)
		if err != nil {
			t.Fatalf("buildJSONLIndex %s: %v", file.path, err)
		}
		indexes = append(indexes, index)
	}

	store := &JSONL{Fs: fs, sessionId: "session-a"}
	store.Owners.Store("owner-a", &ownerJSONL{
		sessionId: "session-a",
		indexes:   indexes,
	})
	ctx := owner.WithOwnerId(context.Background(), "owner-a")

	got, err := store.Range(ctx, view.EntryRange{SeqS: 3, SeqE: 8})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if got.Scope != (view.EntryRange{SeqS: 3, SeqE: 8}) {
		t.Fatalf("scope mismatch: got %+v", got.Scope)
	}
	wantIDs := []uint64{3, 5, 7}
	if len(got.Raw) != len(wantIDs) {
		t.Fatalf("entries len mismatch: want %d, got %d", len(wantIDs), len(got.Raw))
	}
	for i, wantID := range wantIDs {
		if got.Raw[i].GetID() != wantID {
			t.Fatalf("entry %d mismatch: want %d, got %d", i, wantID, got.Raw[i].GetID())
		}
	}
}

func TestJSONLRangeRejectsInvalidRange(t *testing.T) {
	t.Parallel()

	store := &JSONL{Fs: afero.NewMemMapFs(), sessionId: "session-a"}
	store.Owners.Store("owner-a", &ownerJSONL{sessionId: "session-a"})
	ctx := owner.WithOwnerId(context.Background(), "owner-a")

	if _, err := store.Range(ctx, view.EntryRange{SeqS: 8, SeqE: 3}); err == nil {
		t.Fatal("Range invalid range: want error, got nil")
	}
}

func TestJSONLCallbackRequiresSemanticIndex(t *testing.T) {
	t.Parallel()

	store, err := NewJSONLStorage("session-a", "/tapes")
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Callback(ctx, "query", 1); err == nil {
		t.Fatal("Callback without model: want error")
	}

	disabled, err := NewJSONLStorage("session-a", "/tapes-disabled")
	if err != nil {
		t.Fatal(err)
	}
	disabled.Fs = afero.NewMemMapFs()
	model := &fakeSemanticModel{enabled: false}
	ctx = llm.WithModel(owner.WithOwnerId(context.Background(), "owner-a"), model)
	if err := disabled.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := disabled.Callback(ctx, "query", 1); err == nil {
		t.Fatal("Callback with disabled model: want error")
	}
}

func TestJSONLCallbackReranksSemanticMatches(t *testing.T) {
	t.Parallel()

	model := &fakeSemanticModel{
		enabled: true,
		vectors: map[string][]float32{
			"query": {1, 0},
			"near":  {1, 0},
			"also":  {1, 0},
			"far":   {0, 1},
		},
		reranked: []string{"also", "near"},
	}
	store, ctx := newSemanticStore(t, model, "/tapes")
	for _, text := range []string{"near", "also", "far"} {
		if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent(text))); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.Callback(ctx, "query", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("Callback views len: want 2, got %d", len(got))
	}
	if got[0].Raw[0].GetSummary() != "also" || got[1].Raw[0].GetSummary() != "near" {
		t.Fatalf("Callback rerank order mismatch: got %q, %q", got[0].Raw[0].GetSummary(), got[1].Raw[0].GetSummary())
	}
}

func TestJSONLCallbackReturnsHandoffAnchorRange(t *testing.T) {
	t.Parallel()

	model := &fakeSemanticModel{
		enabled: true,
		vectors: map[string][]float32{
			"archive": {1, 0},
			"old":     {0, 1},
			"new":     {0, 1},
		},
	}
	store, ctx := newSemanticStore(t, model, "/tapes")
	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("old"))); err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("new"))); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(entry.HandoffAnchor{Summary: "archive", SeqS: 1, SeqE: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(ctx, entry.NewAnchor(0, "owner-a", entry.AnchorKindHandoff, payload)); err != nil {
		t.Fatal(err)
	}

	got, err := store.Callback(ctx, "archive", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Raw) != 2 {
		t.Fatalf("Callback handoff range mismatch: views=%d raw=%v", len(got), got)
	}
	if got[0].Raw[0].GetSummary() != "old" || got[0].Raw[1].GetSummary() != "new" {
		t.Fatalf("Callback handoff entries mismatch: %#v", got[0].Raw)
	}
}

func TestJSONLSearchUsesCallback(t *testing.T) {
	t.Parallel()

	model := &fakeSemanticModel{
		enabled: true,
		vectors: map[string][]float32{
			"query": {1, 0},
			"hit":   {1, 0},
		},
	}
	store, ctx := newSemanticStore(t, model, "/tapes")
	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("hit"))); err != nil {
		t.Fatal(err)
	}

	got, err := store.Search(ctx, storage.WithSemanticPrompt("query"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Raw) != 1 || got.Raw[0].GetSummary() != "hit" {
		t.Fatalf("Search semantic mismatch: %#v", got.Raw)
	}
}

func TestJSONLCallbackReturnsReRankError(t *testing.T) {
	t.Parallel()

	want := errors.New("rerank failed")
	model := &fakeSemanticModel{
		enabled:   true,
		vectors:   map[string][]float32{"query": {1, 0}, "hit": {1, 0}},
		rerankErr: want,
	}
	store, ctx := newSemanticStore(t, model, "/tapes")
	if err := store.Store(ctx, entry.NewEntry(entry.WithEntryContent("hit"))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Callback(ctx, "query", 1); !errors.Is(err, want) {
		t.Fatalf("Callback rerank error: want %v, got %v", want, err)
	}
}

func mustOwnerState(t *testing.T, store *JSONL, ownerID string) *ownerJSONL {
	t.Helper()
	value, ok := store.Owners.Load(ownerID)
	if !ok {
		t.Fatalf("owner state %q not found", ownerID)
	}
	return value.(*ownerJSONL)
}

func newSemanticStore(t *testing.T, model *fakeSemanticModel, path string) (*JSONL, context.Context) {
	t.Helper()
	store, err := NewJSONLStorage("session-a", path)
	if err != nil {
		t.Fatal(err)
	}
	store.Fs = afero.NewMemMapFs()
	ctx := llm.WithModel(owner.WithOwnerId(context.Background(), "owner-a"), model)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

type fakeSemanticModel struct {
	enabled   bool
	vectors   map[string][]float32
	reranked  []string
	rerankErr error
}

func (m *fakeSemanticModel) IsEnable() bool {
	return m.enabled
}

func (m *fakeSemanticModel) Embedding(_ context.Context, text string) ([]float32, error) {
	if v, ok := m.vectors[text]; ok {
		return v, nil
	}
	return []float32{0, 1}, nil
}

func (m *fakeSemanticModel) ReRank(_ context.Context, _ string, candidates []string) ([]string, error) {
	if m.rerankErr != nil {
		return nil, m.rerankErr
	}
	if m.reranked != nil {
		return m.reranked, nil
	}
	return candidates, nil
}
