package jsonl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadLinesKeepsJSONLBoundaries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "entries.jsonl")
	content := []byte("{\"seq\":0}\n{\"seq\":1}\n{\"seq\":2}\n")
	if err := os.WriteFile(file, content, 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	data, err := readLines(context.Background(), file, 1, int64(LINE_EOF))
	if err != nil {
		t.Fatalf("readLines to EOF: %v", err)
	}

	want := "{\"seq\":1}\n{\"seq\":2}\n"
	if string(data) != want {
		t.Fatalf("readLines mismatch:\nwant %q\ngot  %q", want, string(data))
	}
}

func TestJSONLReadAcrossFilesStartsFromRequestedFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	f0 := filepath.Join(dir, "0.jsonl")
	f1 := filepath.Join(dir, "1.jsonl")
	f2 := filepath.Join(dir, "2.jsonl")

	if err := os.WriteFile(f0, []byte("f0-l0\n"), 0o644); err != nil {
		t.Fatalf("write file 0: %v", err)
	}
	if err := os.WriteFile(f1, []byte("f1-l0\nf1-l1\n"), 0o644); err != nil {
		t.Fatalf("write file 1: %v", err)
	}
	if err := os.WriteFile(f2, []byte("f2-l0\nf2-l1\n"), 0o644); err != nil {
		t.Fatalf("write file 2: %v", err)
	}

	store := &JSONL{
		files: []string{f0, f1, f2},
	}

	data, err := store.Read(context.Background(), fIndex{fileIndex: 1, lineIndex: 1}, fIndex{fileIndex: 2, lineIndex: 0})
	if err != nil {
		t.Fatalf("read across files: %v", err)
	}

	want := "f1-l1\nf2-l0\n"
	if string(data) != want {
		t.Fatalf("Read mismatch:\nwant %q\ngot  %q", want, string(data))
	}
}

func TestJSONLInitCreatesSessionFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store, err := NewJSONLStorage("session-a", dir)
	if err != nil {
		t.Fatalf("NewJSONLStorage: %v", err)
	}

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if len(store.files) != 1 {
		t.Fatalf("files len mismatch: want 1, got %d", len(store.files))
	}

	gotFile := store.files[0]
	if _, err := os.Stat(gotFile); err != nil {
		t.Fatalf("stat created file: %v", err)
	}

	wantDir := filepath.Join(dir, "session-a")
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

	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	if len(store.files) != 1 {
		t.Fatalf("files len mismatch after repeated Init: want 1, got %d", len(store.files))
	}
}
