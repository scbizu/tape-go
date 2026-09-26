package jev

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/finder"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

var (
	_ storage.TapeStorage = (*Storage)(nil)
	_ finder.Provider     = (*Storage)(nil)
)

type Config struct {
	Decider    AnchorDecider
	Summarizer llm.Summarizer
	Classifier Classifier
	Threshold  float64
	OnError    func(error)
}

type snapshotKey struct {
	owner, session string
}

// Storage decorates a tape with JEV anchoring and search. All ordinary storage
// methods are delegated to the wrapped storage.
type Storage struct {
	storage.TapeStorage
	View       *view.EntryView
	policy     AnchorPolicy
	classifier Classifier
	onError    func(error)

	storeMu   sync.Mutex
	mu        sync.RWMutex
	snapshots map[snapshotKey]Snapshot
}

func NewStorage(base storage.TapeStorage, activeView *view.EntryView, config Config) (*Storage, error) {
	if base == nil || activeView == nil {
		return nil, errors.New("jev: storage and active view are required")
	}
	policy := NewAnchorPolicy(config.Decider, config.Summarizer, config.Threshold)
	if err := validateStructure("anchor policy", policy); err != nil {
		return nil, err
	}
	if config.Classifier == nil {
		return nil, errors.New("jev: classifier is required")
	}
	return &Storage{
		TapeStorage: base,
		View:        activeView,
		policy:      policy,
		classifier:  config.Classifier,
		onError:     config.OnError,
		snapshots:   make(map[snapshotKey]Snapshot),
	}, nil
}

func (s *Storage) Unwrap() storage.TapeStorage { return s.TapeStorage }

func (s *Storage) Close() error {
	if closer, ok := s.TapeStorage.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (s *Storage) Finder(query string) finder.Engine {
	return Finder{Query: query, Classifier: s.classifier, storage: s}
}

// Init rebuilds the active JEV frontier from persisted anchors. The rebuilt
// snapshot becomes visible only after the complete enumeration succeeds.
func (s *Storage) Init(ctx context.Context) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	if err := s.TapeStorage.Init(ctx); err != nil {
		return err
	}
	key, err := s.key(ctx)
	if err != nil {
		return err
	}
	var next Snapshot
	var previous entry.Seq
	for anchor, err := range s.TapeStorage.Anchors(ctx) {
		if err != nil {
			return fmt.Errorf("jev: restore anchors: %w", err)
		}
		if anchor == nil {
			return errors.New("jev: restore anchors: nil anchor")
		}
		if !previous.IsZero() && anchor.GetID().Cmp(previous) <= 0 {
			return errors.New("jev: anchors are not in ascending sequence order")
		}
		previous = anchor.GetID()
		if anchor.GetKind() != Kind {
			continue
		}
		record, err := AnchorFromEntry(anchor)
		if err != nil {
			return err
		}
		next.Apply(record)
	}
	s.mu.Lock()
	s.snapshots[key] = next
	s.mu.Unlock()
	return nil
}

func (s *Storage) Store(ctx context.Context, e entry.EntryLike) (entry.EntryLike, error) {
	if e == nil {
		return s.TapeStorage.Store(ctx, e)
	}
	if e.GetKind().IsAnchor() {
		s.storeMu.Lock()
		stored, err := s.TapeStorage.Store(ctx, e)
		var applyErr error
		if err == nil && e.GetKind() == Kind {
			applyErr = s.applyStored(ctx, stored)
		}
		s.storeMu.Unlock()
		if applyErr != nil {
			s.report(applyErr)
		}
		return stored, err
	}

	active := *s.View
	s.storeMu.Lock()
	stored, err := s.TapeStorage.Store(ctx, e)
	s.storeMu.Unlock()
	if err != nil {
		return nil, err
	}
	if stored == nil {
		s.report(errors.New("jev: stored entry is nil"))
		return e, nil
	}
	if stored.GetID().IsZero() {
		s.report(errors.New("jev: stored entry has no ID"))
		return stored, nil
	}
	start := active.Scope.SeqS
	if start.IsZero() {
		start = entry.SeqFromUint64(1)
	}
	if start.Cmp(stored.GetID()) > 0 {
		start = stored.GetID()
	}
	memory, err := s.TapeStorage.Range(ctx, view.EntryRange{SeqS: start, SeqE: stored.GetID().Next()})
	if err != nil {
		s.report(fmt.Errorf("jev: assemble anchor view: %w", err))
		return stored, nil
	}
	anchor, ok, err := s.policy.MakeAnchor(ctx, stored, memory)
	if err != nil {
		s.report(fmt.Errorf("jev: anchor policy: %w", err))
		return stored, nil
	}
	if !ok {
		return stored, nil
	}
	if anchor == nil {
		s.report(errors.New("jev: anchor policy returned nil anchor"))
		return stored, nil
	}
	s.storeMu.Lock()
	persisted, err := s.TapeStorage.Store(ctx, anchor)
	var applyErr error
	if err == nil {
		applyErr = s.applyStored(ctx, persisted)
	}
	s.storeMu.Unlock()
	if err != nil {
		s.report(fmt.Errorf("jev: store derived anchor: %w", err))
	} else if applyErr != nil {
		s.report(applyErr)
	}
	return stored, nil
}

func (s *Storage) key(ctx context.Context) (snapshotKey, error) {
	v, err := s.TapeStorage.Get(ctx)
	if err != nil {
		return snapshotKey{}, err
	}
	return snapshotKey{owner: v.Owner, session: v.SessionID}, nil
}

func (s *Storage) snapshot(ctx context.Context) (Snapshot, error) {
	key, err := s.key(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	snapshot, ok := s.snapshots[key]
	result := snapshot.Clone()
	s.mu.RUnlock()
	if !ok {
		return Snapshot{}, errors.New("jev: storage is not initialized for this owner and session")
	}
	return result, nil
}

func (s *Storage) applyStored(ctx context.Context, stored entry.EntryLike) error {
	if stored == nil {
		return errors.New("jev: stored anchor is nil")
	}
	record, err := AnchorFromEntry(stored)
	if err != nil {
		return err
	}
	key, err := s.key(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	snapshot, ok := s.snapshots[key]
	if ok {
		snapshot.Apply(record)
		s.snapshots[key] = snapshot
	}
	s.mu.Unlock()
	if !ok {
		return errors.New("jev: storage is not initialized for this owner and session")
	}
	return nil
}

func (s *Storage) report(err error) {
	if s.onError != nil {
		s.onError(err)
	}
}
