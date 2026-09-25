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

func (s *AnchoringStorage) Store(ctx context.Context, e entry.EntryLike) error {
	if s.View == nil || e == nil || e.GetKind().IsAnchor() {
		return s.TapeStorage.Store(ctx, e)
	}
	active := *s.View
	if active.AnchorMaker == nil {
		return s.TapeStorage.Store(ctx, e)
	}

	s.mu.Lock()
	if err := s.TapeStorage.Store(ctx, e); err != nil {
		s.mu.Unlock()
		return err
	}
	seq := e.GetID()
	if seq.IsZero() {
		tv, err := s.TapeStorage.Get(ctx)
		if err != nil {
			s.mu.Unlock()
			s.report(fmt.Errorf("storage: resolve anchor scope: %w", err))
			return nil
		}
		seq = tv.Scope.SeqE
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
		return nil
	}
	memory.AnchorMaker = active.AnchorMaker
	anchor, ok, err := memory.MakeAnchor(ctx, e)
	if err != nil {
		s.report(fmt.Errorf("storage: anchor policy: %w", err))
		return nil
	}
	if !ok {
		return nil
	}
	if anchor == nil {
		s.report(errors.New("storage: anchor maker returned nil anchor"))
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.TapeStorage.Store(ctx, anchor); err != nil {
		s.report(fmt.Errorf("storage: store derived anchor: %w", err))
	}
	return nil
}

func (s *AnchoringStorage) report(err error) {
	if s.OnError != nil {
		s.OnError(err)
	}
}
