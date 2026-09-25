package storage

import (
	"context"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type resultStorage struct {
	stored     entry.EntryLike
	rangeScope view.EntryRange
	getCalled  bool
}

func (*resultStorage) Init(context.Context) error { return nil }
func (s *resultStorage) Get(context.Context) (view.TapeView, error) {
	s.getCalled = true
	return view.TapeView{Scope: view.EntryRange{SeqE: entry.SeqFromUint64(2)}}, nil
}
func (*resultStorage) Store(context.Context, entry.EntryLike) error { panic("legacy Store called") }
func (s *resultStorage) StoreWithResult(_ context.Context, e entry.EntryLike) (entry.EntryLike, error) {
	if e.GetID().IsZero() {
		e = e.WithID(entry.SeqFromUint64(1))
	}
	s.stored = e
	return e, nil
}
func (s *resultStorage) Range(_ context.Context, r view.EntryRange, _ ...RangeBy) (view.EntryView, error) {
	s.rangeScope = r
	return view.EntryView{Raw: []entry.EntryLike{s.stored}, Scope: r}, nil
}
func (*resultStorage) Rewind(context.Context, ...RewindBy) (view.EntryRange, error) {
	return view.EntryRange{}, nil
}

type anchorMakerFunc func(context.Context, entry.EntryLike, view.EntryView) (entry.EntryLike, bool, error)

func (f anchorMakerFunc) MakeAnchor(ctx context.Context, e entry.EntryLike, v view.EntryView) (entry.EntryLike, bool, error) {
	return f(ctx, e, v)
}

func TestAnchoringUsesStoredEntryResult(t *testing.T) {
	base := &resultStorage{}
	active := view.EntryView{AnchorMaker: anchorMakerFunc(func(_ context.Context, latest entry.EntryLike, memory view.EntryView) (entry.EntryLike, bool, error) {
		if latest.GetID() != entry.SeqFromUint64(1) || len(memory.Raw) != 1 || memory.Raw[0].GetID() != latest.GetID() {
			t.Errorf("anchor maker received the wrong stored entry: latest=%v, view=%v", latest, memory.Raw)
		}
		return nil, false, nil
	})}
	decorated := NewAnchoringStorage(base, &active, nil)
	stored, err := decorated.StoreWithResult(context.Background(), entry.NewEntry(entry.WithEntryContent("first")))
	if err != nil {
		t.Fatal(err)
	}
	if stored.GetID() != entry.SeqFromUint64(1) {
		t.Fatalf("decorated store returned ID %s, want 1", stored.GetID())
	}
	if base.getCalled {
		t.Fatal("anchoring read the latest tape ID instead of the stored entry result")
	}
	if want := (view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)}); base.rangeScope != want {
		t.Fatalf("anchor scope = %v, want %v", base.rangeScope, want)
	}
}

func TestAnchoringLegacyStoreForwardsToResult(t *testing.T) {
	base := &resultStorage{}
	decorated := NewAnchoringStorage(base, nil, nil)
	if err := decorated.Store(context.Background(), entry.NewEntry()); err != nil {
		t.Fatal(err)
	}
	if base.stored == nil || base.stored.GetID() != entry.SeqFromUint64(1) {
		t.Fatalf("legacy Store did not delegate to StoreWithResult: %v", base.stored)
	}
}
