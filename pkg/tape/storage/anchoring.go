package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// AnchoringStorage decorates a TapeStorage with optional post-store anchor
// creation. Store remains a storage operation; Tape does not participate in
// the lifecycle.
type AnchoringStorage struct {
	TapeStorage
	View    *view.EntryView
	OnError func(error)

	mu sync.Mutex
}

func NewAnchoringStorage(base TapeStorage, activeView *view.EntryView, onError func(error)) *AnchoringStorage {
	return &AnchoringStorage{TapeStorage: base, View: activeView, OnError: onError}
}

func (s *AnchoringStorage) Unwrap() TapeStorage {
	return s.TapeStorage
}

func (s *AnchoringStorage) Close() error {
	if closer, ok := s.TapeStorage.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

// Store is retained for callers that only need an error.
// Deprecated: use StoreWithResult.
func (s *AnchoringStorage) Store(ctx context.Context, e entry.EntryLike) error {
	_, err := s.StoreWithResult(ctx, e)
	return err
}

func (s *AnchoringStorage) StoreWithResult(ctx context.Context, e entry.EntryLike) (entry.EntryLike, error) {
	if s.View == nil || e == nil || e.GetKind().IsAnchor() {
		return s.TapeStorage.StoreWithResult(ctx, e)
	}
	active := *s.View
	if active.AnchorMaker == nil {
		return s.TapeStorage.StoreWithResult(ctx, e)
	}

	s.mu.Lock()
	stored, err := s.TapeStorage.StoreWithResult(ctx, e)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if stored == nil {
		s.mu.Unlock()
		s.report(errors.New("storage: store returned nil entry"))
		return e, nil
	}
	seq := stored.GetID()
	if seq.IsZero() {
		s.mu.Unlock()
		s.report(errors.New("storage: cannot anchor entry without a known stored ID"))
		return stored, nil
	}
	s.mu.Unlock()

	start := active.Scope.SeqS
	if start.IsZero() {
		start = entry.SeqFromUint64(1)
	}
	if start.Cmp(seq) > 0 {
		start = seq
	}
	memory, err := s.TapeStorage.Range(ctx, view.EntryRange{SeqS: start, SeqE: seq.Next()})
	if err != nil {
		s.report(fmt.Errorf("storage: assemble anchor view: %w", err))
		return stored, nil
	}
	memory.AnchorMaker = active.AnchorMaker
	anchor, ok, err := memory.MakeAnchor(ctx, stored)
	if err != nil {
		s.report(fmt.Errorf("storage: anchor policy: %w", err))
		return stored, nil
	}
	if !ok {
		return stored, nil
	}
	if anchor == nil {
		s.report(errors.New("storage: anchor maker returned nil anchor"))
		return stored, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.TapeStorage.StoreWithResult(ctx, anchor); err != nil {
		s.report(fmt.Errorf("storage: store derived anchor: %w", err))
	}
	return stored, nil
}

func (s *AnchoringStorage) report(err error) {
	if s.OnError != nil {
		s.OnError(err)
	}
}
