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
	"strings"
	"sync"
	"time"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
	"github.com/spf13/afero"
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
		Fs:              afero.NewOsFs(),
		localPathPrefix: abs,
		sessionId:       sessionId,
	}, nil
}

// JSONLIndex is a sparse index that maps one JSONL file to its entry sequence range.
type JSONLIndex struct {
	Path    string
	Scope   view.EntryRange
	Entries uint64
}

type ownerJSONL struct {
	sync.RWMutex
	sessionId   string
	lastEntryId uint64
	indexes     []JSONLIndex
}

type JSONL struct {
	afero.Fs

	Owners          sync.Map // OwnerId -> *ownerJSONL
	localPathPrefix string
	sessionId       string
}

var LINE_EOF = -1

// readLines reads file's [l1:l2] lines
func readLines(_ context.Context, fs afero.Fs, f string, l1, l2 int64) ([]byte, error) {
	fd, err := fs.Open(f)
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

// Init a JSONL or load existing JSONL storage
// Filepath must be like: {localPathPrefix}/{owner}/{sessionId}/{FILES}
func (j *JSONL) Init(
	ctx context.Context,
) error {
	owr, state, err := j.ownerState(ctx, true)
	if err != nil {
		return err
	}
	state.Lock()
	defer state.Unlock()

	path := filepath.Join(
		j.localPathPrefix,
		owr,
		state.sessionId,
	)
	info, err := j.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := j.MkdirAll(
				path,
				0o700,
			); err != nil {
				return fmt.Errorf("jsonl: init: %w", err)
			}
			info, err = j.Stat(path)
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
		if _, err := j.Stat(ff); err != nil {
			if os.IsNotExist(err) {
				fd, ferr := j.OpenFile(
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
		dirEntries, err := afero.ReadDir(j.Fs, path)
		if err != nil {
			return fmt.Errorf("jsonl: read session directory: %w", err)
		}
		indexes := make([]JSONLIndex, 0, len(dirEntries))
		for _, dirEntry := range dirEntries {
			if dirEntry.IsDir() || !strings.HasSuffix(dirEntry.Name(), ".jsonl") {
				continue
			}
			index, err := buildJSONLIndex(j.Fs, filepath.Join(path, dirEntry.Name()))
			if err != nil {
				return fmt.Errorf("jsonl: build index: %w", err)
			}
			indexes = append(indexes, index)
		}
		state.indexes = indexes
		if len(indexes) > 0 {
			state.lastEntryId = indexes[len(indexes)-1].Scope.SeqE
		}
	}
	return nil
}

func (j *JSONL) Get(
	ctx context.Context,
) (view.TapeView, error) {
	owr, state, err := j.ownerState(ctx, false)
	if err != nil {
		return view.TapeView{}, err
	}
	state.RLock()
	defer state.RUnlock()

	return view.TapeView{
		SessionID: state.sessionId,
		Owner:     owr,
		Scope: view.EntryRange{
			SeqE: state.lastEntryId,
		},
	}, nil
}

func (j *JSONL) Store(
	ctx context.Context,
	e entry.Entry,
) error {
	_, state, err := j.ownerState(ctx, false)
	if err != nil {
		return err
	}
	state.Lock()
	defer state.Unlock()

	if len(state.indexes) == 0 {
		return errors.New("jsonl: no index to store")
	}
	index := &state.indexes[len(state.indexes)-1]
	// append e to the file
	fd, err := j.OpenFile(index.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("jsonl: open file: %w", err)
	}
	defer fd.Close()
	if err := json.NewEncoder(fd).Encode(e); err != nil {
		return fmt.Errorf("jsonl: encodes entry to storage failed: %w", err)
	}
	if index.Entries == 0 {
		index.Scope.SeqS = e.GetID()
	}
	index.Scope.SeqE = e.GetID()
	index.Entries++
	state.lastEntryId = e.GetID()
	return nil
}

func buildJSONLIndex(fs afero.Fs, path string) (JSONLIndex, error) {
	fd, err := fs.Open(path)
	if err != nil {
		return JSONLIndex{}, err
	}
	defer fd.Close()

	index := JSONLIndex{Path: path}
	decoder := json.NewDecoder(fd)
	for {
		var e entry.Entry
		if err := decoder.Decode(&e); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return JSONLIndex{}, fmt.Errorf("decode %s: %w", path, err)
		}
		if index.Entries == 0 {
			index.Scope.SeqS = e.GetID()
		}
		index.Scope.SeqE = e.GetID()
		index.Entries++
	}
	return index, nil
}

func (j *JSONL) ownerState(ctx context.Context, create bool) (string, *ownerJSONL, error) {
	ownerID, err := owner.GetOwnerId(ctx)
	if err != nil {
		return "", nil, err
	}

	if state, ok := j.Owners.Load(ownerID); ok {
		return ownerID, state.(*ownerJSONL), nil
	}
	if !create {
		return "", nil, fmt.Errorf("jsonl: owner %q is not initialized", ownerID)
	}

	state, _ := j.Owners.LoadOrStore(ownerID, &ownerJSONL{
		sessionId: j.sessionId,
	})
	return ownerID, state.(*ownerJSONL), nil
}

func (j *JSONL) Range(
	ctx context.Context,
	r view.EntryRange,
) (view.EntryView, error) {
	if r.SeqS > r.SeqE {
		return view.EntryView{}, fmt.Errorf(
			"jsonl: invalid range [%d,%d)",
			r.SeqS,
			r.SeqE,
		)
	}
	owr, state, err := j.ownerState(ctx, false)
	if err != nil {
		return view.EntryView{}, fmt.Errorf("jsonl: %w", err)
	}
	state.RLock()
	defer state.RUnlock()

	v := view.EntryView{
		SessionId: j.sessionId,
		Owner:     owr,
		Scope:     r,
	}
	if r.SeqS == r.SeqE {
		return v, nil
	}

	for _, index := range state.indexes {
		if index.Entries == 0 ||
			index.Scope.SeqE < r.SeqS ||
			index.Scope.SeqS >= r.SeqE {
			continue
		}
		entries, err := j.readEntriesInRange(ctx, index.Path, r)
		if err != nil {
			return view.EntryView{}, fmt.Errorf("jsonl: range: %w", err)
		}
		v.Raw = append(v.Raw, entries...)
	}
	return v, nil
}

func (j *JSONL) Rewind(ctx context.Context, seq int) (view.EntryRange, error) {
	if seq <= 0 {
		return view.EntryRange{}, fmt.Errorf("jsonl: rewind: invalid seq %d", seq)
	}

	_, state, err := j.ownerState(ctx, false)
	if err != nil {
		return view.EntryRange{}, fmt.Errorf("jsonl: %w", err)
	}
	state.RLock()
	defer state.RUnlock()

	seqID := uint64(seq)
	for i := len(state.indexes) - 1; i >= 0; i-- {
		index := state.indexes[i]
		if index.Entries == 0 || index.Scope.SeqS > seqID {
			continue
		}
		r, ok, err := j.rewindIndex(ctx, index.Path, seqID)
		if err != nil {
			return view.EntryRange{}, fmt.Errorf("jsonl: rewind: %w", err)
		}
		if ok {
			return r, nil
		}
	}
	return view.EntryRange{}, fmt.Errorf("jsonl: rewind: no handoff anchor before seq %d", seq)
}

func (j *JSONL) rewindIndex(
	ctx context.Context,
	path string,
	seq uint64,
) (view.EntryRange, bool, error) {
	fd, err := j.Open(path)
	if err != nil {
		return view.EntryRange{}, false, fmt.Errorf("open %s: %w", path, err)
	}
	defer fd.Close()

	var latest entry.Entry
	decoder := json.NewDecoder(fd)
	for {
		if err := ctx.Err(); err != nil {
			return view.EntryRange{}, false, err
		}

		var e entry.Entry
		if err := decoder.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return view.EntryRange{}, false, fmt.Errorf("decode %s: %w", path, err)
		}
		if e.GetID() <= seq && e.GetKind() == entry.EntryKind(entry.AnchorKindHandoff.String()) {
			latest = e
		}
	}
	if latest.GetID() == 0 {
		return view.EntryRange{}, false, nil
	}

	var anchor entry.HandoffAnchor
	if err := json.Unmarshal([]byte(latest.Text), &anchor); err != nil {
		return view.EntryRange{}, false, fmt.Errorf("decode handoff anchor %d: %w", latest.GetID(), err)
	}
	return view.EntryRange{
		SeqS: anchor.SeqS,
		SeqE: anchor.SeqE,
	}, true, nil
}

func (j *JSONL) readEntriesInRange(
	ctx context.Context,
	path string,
	r view.EntryRange,
) ([]entry.Entry, error) {
	fd, err := j.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer fd.Close()

	var entries []entry.Entry
	decoder := json.NewDecoder(fd)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var e entry.Entry
		if err := decoder.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if e.GetID() >= r.SeqS && e.GetID() < r.SeqE {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

func (j *JSONL) Search(
	ctx context.Context,
	opts ...storage.SearchBy,
) (view.EntryView, error) {
	return view.EntryView{}, errors.New("jsonl: do not support semantic search for now")
}
