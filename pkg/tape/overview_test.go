package tape

import (
	"context"
	"errors"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type overviewHandler struct {
	handle func(context.Context, []*view.EntryView, any) (any, error)
}

func (*overviewHandler) Action() string { return "test" }
func (h *overviewHandler) Handle(ctx context.Context, views []*view.EntryView, payload any) (any, error) {
	return h.handle(ctx, views, payload)
}

func TestTapeOverView(t *testing.T) {
	tape := newMemoryTape(t, "owner-a", "session-a")
	for _, payload := range []string{`"first"`, `"second"`, `"third"`} {
		if _, err := tape.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	scope := view.EntryRange{SeqS: entry.SeqFromUint64(2), SeqE: entry.SeqFromUint64(3)}
	tape.Registry = view.NewRegistry()
	before := tape.View.Scope
	ov, err := tape.OverView(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if ov.SessionId != "session-a" || ov.Owner != "owner-a" || ov.Scope != scope || len(ov.Raw) != 1 {
		t.Fatalf("OverView = %#v", ov.EntryView)
	}
	if ov.Raw[0].GetID() != scope.SeqS {
		t.Fatalf("OverView entry ID = %s, want %s", ov.Raw[0].GetID(), scope.SeqS)
	}
	if tape.View.Scope != before || tape.readBuf != nil || !tape.readSeq.IsZero() {
		t.Fatal("OverView changed the tape's read state")
	}
	payload := new(int)
	if err := tape.Registry.Register(&overviewHandler{handle: func(gotCtx context.Context, views []*view.EntryView, p any) (any, error) {
		if gotCtx != ctx || len(views) != 1 || views[0] != &ov.EntryView || p != payload {
			t.Fatal("handler arguments were not preserved")
		}
		return views[0].Raw[0].GetID(), nil
	}}); err != nil {
		t.Fatal(err)
	}
	if got, err := ov.Handle(ctx, "test", payload); err != nil || got != scope.SeqS {
		t.Fatalf("Handle = %v, %v", got, err)
	}

	// A tape without a registry still exposes the storage view.
	tape.Registry = nil
	unbound, err := tape.OverView(ctx, scope)
	if err != nil || len(unbound.Raw) != 1 {
		t.Fatalf("OverView without registry = %#v, %v", unbound, err)
	}
	if _, err := unbound.Handle(ctx, "test", nil); err == nil {
		t.Fatal("unconfigured tape used another view's registry")
	}
}

type overviewRangeStorage struct {
	noopStorage
	rangeFn func(context.Context, view.EntryRange) (view.EntryView, error)
}

func (s overviewRangeStorage) Range(ctx context.Context, scope view.EntryRange, _ ...storage.RangeBy) (view.EntryView, error) {
	return s.rangeFn(ctx, scope)
}

func TestTapeOverViewRangeErrors(t *testing.T) {
	for _, wantErr := range []error{errors.New("range failed"), context.Canceled} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if wantErr == context.Canceled {
				cancel()
			}
			scope := view.EntryRange{SeqS: entry.SeqFromUint64(4), SeqE: entry.SeqFromUint64(5)}
			tape := &Tape{TapeStorage: overviewRangeStorage{rangeFn: func(gotCtx context.Context, gotScope view.EntryRange) (view.EntryView, error) {
				if gotCtx != ctx || gotScope != scope {
					t.Fatal("OverView did not forward context and scope unchanged")
				}
				return view.EntryView{}, wantErr
			}}}
			if _, err := tape.OverView(ctx, scope); err != wantErr {
				t.Fatalf("OverView error = %v, want %v", err, wantErr)
			}
		})
	}
	for _, tape := range []*Tape{nil, {}} {
		if _, err := tape.OverView(context.Background(), view.EntryRange{}); err == nil {
			t.Fatal("OverView accepted nil storage")
		}
	}
}

func TestTapeOverViewMultipleRanges(t *testing.T) {
	tape := newMemoryTape(t, "owner-a", "session-a")
	for _, payload := range []string{`"first"`, `"second"`, `"third"`} {
		if _, err := tape.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	ctx := owner.WithOwnerId(context.Background(), "owner-a")
	tape.Registry = view.NewRegistry()
	first, err := tape.OverView(ctx, view.EntryRange{
		SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := tape.OverView(ctx, view.EntryRange{
		SeqS: entry.SeqFromUint64(3), SeqE: entry.SeqFromUint64(4),
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := tape.Registry.Register(&overviewHandler{handle: func(_ context.Context, views []*view.EntryView, _ any) (any, error) {
		calls++
		if len(views) != 2 || views[0] != &first.EntryView || views[1] != &third.EntryView {
			t.Fatal("handler did not receive both storage views in order")
		}
		for i, want := range []uint64{1, 3} {
			if len(views[i].Raw) != 1 || views[i].Raw[0].GetID() != entry.SeqFromUint64(want) {
				t.Fatalf("unexpected entries for view %d: %#v", i, views[i])
			}
		}
		return len(views), nil
	}}); err != nil {
		t.Fatal(err)
	}
	if got, err := first.Handle(ctx, "test", nil, &third.EntryView); got != 2 || err != nil || calls != 1 {
		t.Fatalf("Handle = %v, %v; calls = %d", got, err, calls)
	}
}
