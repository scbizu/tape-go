// Package jsonl provides the JSONL TAPE Storage
package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/storage"
)

var _ storage.TapeStorage = (*JSONL)(nil)

func NewJSONLStorage(sessionId string, lp string) (*JSONL, error) {
	if lp == "" {
		return nil, fmt.Errorf("jsonl: empty filepath")
	}
	abs, err := filepath.Abs(lp)
	if err != nil {
		return nil, fmt.Errorf("jsonl: bad filepath: %w", err)
	}
	return &JSONL{
		localPathPrefix: abs,
		sessionId:       sessionId,
		Mutex:           &sync.Mutex{},
	}, nil
}

// JSONLIndex is a range-able index designed for JSONL
// Rather than keep all entries in the same file
// `JSONLIndex` uses `readline()` to quickly read file blocks by file blocks.
//
//	such as `F{HASH_1}:L1 ~ F{HASH_1}L20` or `F{HASH_1}:L1023 ~ (F{HASH_1}:EOF) ~ F{HASH_2}:L2`
type JSONLIndex struct {
	indexHash string
	start     fIndex
	end       fIndex
}

type fIndex struct {
	fileIndex uint64
	lineIndex uint32
}

func (fi fIndex) String() string {
	return fmt.Sprintf("F%d:L%d", fi.fileIndex, fi.lineIndex)
}

func NewIndex(hash string) JSONLIndex {
	if hash == "" {
		hash = uuid.NewString()
	}
	return JSONLIndex{
		indexHash: hash,
	}
}

type JSONL struct {
	*sync.Mutex

	sessionId       string
	localPathPrefix string
	// in order to keep better r/w
	// we split the single session into different files
	files []string

	index JSONLIndex
}

var LINE_EOF = -1

func (j *JSONL) Read(ctx context.Context, fi1, fi2 fIndex) ([]byte, error) {
	if fi1.fileIndex >= uint64(len(j.files)) || fi2.fileIndex >= uint64(len(j.files)) {
		return nil, fmt.Errorf("read: file index out of range")
	}
	if fi1.fileIndex > fi2.fileIndex {
		return nil, fmt.Errorf("read: invalid range %s -> %s", fi1, fi2)
	}
	if fi1.fileIndex == fi2.fileIndex && fi1.lineIndex > fi2.lineIndex {
		return nil, fmt.Errorf("read: invalid range %s -> %s", fi1, fi2)
	}
	if fi1.fileIndex == fi2.fileIndex {
		return readLines(
			ctx,
			j.files[fi1.fileIndex],
			int64(fi1.lineIndex),
			int64(fi2.lineIndex),
		)
	}
	buf := bytes.NewBuffer(nil)
	for i := fi1.fileIndex; i <= fi2.fileIndex; i++ {
		start := int64(0)
		end := int64(LINE_EOF)
		if i == fi1.fileIndex {
			start = int64(fi1.lineIndex)
		}
		if i == fi2.fileIndex {
			end = int64(fi2.lineIndex)
		}
		fd, err := readLines(
			ctx,
			j.files[i],
			start,
			end,
		)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		if _, err = buf.Write(fd); err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
	}
	return buf.Bytes(), nil
}

// readLines reads file's [l1:l2] lines
func readLines(_ context.Context, f string, l1, l2 int64) ([]byte, error) {
	fd, err := os.Open(f)
	if err != nil {
		return nil, fmt.Errorf("readLines: %w", err)
	}
	defer fd.Close()
	buf := bytes.NewBuffer(nil)
	reader := bufio.NewReader(fd)
	var lineIndex int64
	// TODO(scnace): we should replace this brute-force solution
	// with `fseek` and cache every line's token (WAL-like)  for better mem allocation
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("readLines: %w", err)
		}
		if errors.Is(err, io.EOF) && len(line) == 0 {
			break
		}
		if lineIndex >= l1 && (l2 == int64(LINE_EOF) || lineIndex <= l2) {
			if _, writeErr := buf.Write(line); writeErr != nil {
				return nil, fmt.Errorf("writeLines: %w", writeErr)
			}
		}
		lineIndex++
		if l2 != int64(LINE_EOF) && lineIndex > l2 {
			break
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return buf.Bytes(), nil
}

// func (j *JSONL) updateIndex(index JSONLIndex) {
// 	j.Mutex.Lock()
// 	defer j.Mutex.Unlock()
// 	j.index = index
// }

// Init a JSONL or load existing JSONL storage
// Filepath must be like: {localPathPrefix}/{sessionId}/{FILES}
func (j *JSONL) Init(
	ctx context.Context,
) error {
	path := filepath.Join(
		j.localPathPrefix,
		string(j.sessionId),
	)
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(
				path,
				0o700,
			); err != nil {
				return fmt.Errorf("jsonl: init: %w", err)
			}
			info, err = os.Stat(path)
			if err != nil {
				return fmt.Errorf("jsonl: stat: %w", err)
			}
		} else {
			return fmt.Errorf("jsonl: init: %w", err)
		}
	}
	if info.IsDir() {
		hash := fmt.Sprintf("%d%d%d", time.Now().Year(), time.Now().Month(), time.Now().Day())
		f := fmt.Sprintf("%s_0.jsonl", hash)
		ff := filepath.Join(path, f)
		if _, err := os.Stat(ff); err != nil {
			if os.IsNotExist(err) {
				fd, ferr := os.OpenFile(
					// create the first index
					ff,
					os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644,
				)
				if ferr != nil {
					return fmt.Errorf("jsonl: bad create jsonl file: %w", ferr)
				}
				defer fd.Close()
			} else {
				return fmt.Errorf("json: stat file: %w", err)
			}
		}
		// j.updateIndex(NewIndex(hash))
		if !slices.Contains(j.files, ff) {
			j.files = append(j.files, ff)
		}
	}
	return nil
}

func (j *JSONL) Get(
	ctx context.Context,
) (storage.TapeView, error) {
	return storage.TapeView{
		HeadAt:    0,
		SessionID: j.sessionId,
	}, nil
}

func (j *JSONL) Store(
	ctx context.Context,
	e entry.Entry,
) error {
	if len(j.files) == 0 {
		return errors.New("jsonl: no files to store")
	}
	// files are always append, so we just need to get the last file handler
	crtFile := j.files[len(j.files)-1]
	// append e to the file
	fd, err := os.OpenFile(crtFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("jsonl: open file: %w", err)
	}
	defer fd.Close()
	if err := json.NewEncoder(fd).Encode(e); err != nil {
		return fmt.Errorf("jsonl: encodes entry to storage failed: %w", err)
	}
	return nil
}

func (j *JSONL) Range(
	ctx context.Context,
	r storage.Range,
) ([]entry.EntryView, error) {
	var evs []entry.EntryView
	return evs, nil
}

func (j *JSONL) Search(
	ctx context.Context,
	opts ...storage.SearchBy,
) ([]entry.EntryView, error) {
	var evs []entry.EntryView
	return evs, nil
}
