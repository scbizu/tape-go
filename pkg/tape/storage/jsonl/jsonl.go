// Package jsonl provides the JSONL TAPE Storage
package jsonl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/storage"
)

var _ storage.TapeStorage = (*JSONL)(nil)

func NewJSONLStorage(lp string) (*JSONL, error) {
	if !filepath.IsLocal(lp) {
		return nil, fmt.Errorf("jsonl: only support local filepath")
	}
	return &JSONL{
		localPath: lp,
	}, nil
}

type JSONL struct {
	localPath string
}

func (j *JSONL) Init(
	ctx context.Context,
	id storage.SessionID,
) error {
	if _, err := os.Stat(j.localPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if derr := os.MkdirAll(filepath.Dir(j.localPath), os.ModeDir); derr != nil {
				return fmt.Errorf("jsonl: bad create jsonl dir: %w", err)
			}
			if _, ferr := os.Create(j.localPath); ferr != nil {
				return fmt.Errorf("jsonl: bad create jsonl file: %w", err)
			}
		}
		return fmt.Errorf("jsonl: fs error on creation: %w", err)
	}
	return nil
}

func (j *JSONL) Get(
	ctx context.Context,
	id storage.SessionID,
) (storage.TapeView, error) {
	return storage.TapeView{}, nil
}

func (j *JSONL) Store(
	ctx context.Context,
	e entry.Entry,
) error {
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
