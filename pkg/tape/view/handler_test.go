package view

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

type testHandler struct {
	action string
	handle func(context.Context, []*EntryView, any) (any, error)
}

func (h *testHandler) Action() string { return h.action }
func (h *testHandler) Handle(ctx context.Context, views []*EntryView, payload any) (any, error) {
	return h.handle(ctx, views, payload)
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	for name, reg := range map[string]*Registry{"new": NewRegistry(), "zero": {}} {
		t.Run(name, func(t *testing.T) {
			if got, ok := reg.Lookup("missing"); ok || got != nil {
				t.Fatalf("Lookup(missing) = %v, %v", got, ok)
			}
			for _, invalid := range []Handler{nil, &testHandler{}} {
				if err := reg.Register(invalid); err == nil {
					t.Fatal("Register accepted an invalid handler")
				}
			}
			if _, ok := reg.Lookup(""); ok {
				t.Fatal("invalid handler was registered")
			}
			h := &testHandler{action: "test"}
			if err := reg.Register(h); err != nil {
				t.Fatal(err)
			}
			for _, duplicate := range []Handler{h, &testHandler{action: "test"}} {
				if err := reg.Register(duplicate); err == nil {
					t.Fatal("Register accepted a duplicate action")
				}
			}
			if got, ok := reg.Lookup("test"); !ok || got != h {
				t.Fatalf("Lookup(test) = %v, %v; want original handler", got, ok)
			}
			if _, ok := reg.Lookup("Test"); ok {
				t.Fatal("action lookup must be exact")
			}
		})
	}
}

func TestRegistryConcurrentRegisterAndLookup(t *testing.T) {
	reg := NewRegistry()
	var wg sync.WaitGroup
	const count = 32
	for i := range count {
		wg.Go(func() {
			h := &testHandler{action: fmt.Sprintf("action-%d", i)}
			if err := reg.Register(h); err != nil {
				t.Error(err)
			}
			if got, ok := reg.Lookup(h.action); !ok || got != h {
				t.Errorf("Lookup(%q) = %v, %v", h.action, got, ok)
			}
			reg.Lookup("missing")
		})
	}
	wg.Wait()
	results := make(chan error, count)
	for range count {
		wg.Go(func() { results <- reg.Register(&testHandler{action: "shared"}) })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent duplicate registration: %d successes, want 1", successes)
	}
}

func TestOverViewHandle(t *testing.T) {
	wantErr := errors.New("handler failed")
	for _, handlerErr := range []error{nil, wantErr, context.Canceled} {
		t.Run(fmt.Sprint(handlerErr), func(t *testing.T) {
			reg := NewRegistry()
			v := EntryView{
				SessionId: "session", Owner: "owner", Summary: "summary",
				Scope: EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)},
				Raw:   []entry.EntryLike{entry.NewEntry(entry.WithEntryContent("hello"))},
			}
			ov := NewOverView([]*EntryView{&v}, reg)
			if len(ov.Views) != 1 || ov.Views[0] != &v {
				t.Fatal("NewOverView changed the view pointer")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if handlerErr == context.Canceled {
				cancel()
			}
			payload, result := new(int), new(int)
			called := 0
			h := &testHandler{action: "test", handle: func(gotCtx context.Context, got []*EntryView, p any) (any, error) {
				called++
				if gotCtx != ctx || len(got) != 1 || got[0] != &v || p != payload {
					t.Fatal("Handle did not forward context, view and payload unchanged")
				}
				// Registration during dispatch must not be blocked by a held read lock.
				if err := reg.Register(&testHandler{action: "other"}); err != nil {
					t.Fatal(err)
				}
				return result, handlerErr
			}}
			// Bind after constructing the view to verify the registry is shared.
			if err := reg.Register(h); err != nil {
				t.Fatal(err)
			}
			got, err := ov.Handle(ctx, "test", payload)
			if got != result || err != handlerErr || called != 1 {
				t.Fatalf("Handle = %v, %v; calls = %d", got, err, called)
			}
		})
	}
}

func TestOverViewHandleMissingAction(t *testing.T) {
	for name, ov := range map[string]OverView{
		"zero": {}, "nil registry": NewOverView(nil, nil),
		"unknown action": NewOverView(nil, NewRegistry()),
	} {
		t.Run(name, func(t *testing.T) {
			for _, action := range []string{"missing", ""} {
				if got, err := ov.Handle(context.Background(), action, nil); got != nil || err == nil {
					t.Fatalf("Handle(%q) = %v, %v; want nil, error", action, got, err)
				}
			}
		})
	}
}

func TestOverViewHandleMultipleViews(t *testing.T) {
	for _, handlerErr := range []error{nil, errors.New("multi-view operation failed")} {
		t.Run(fmt.Sprint(handlerErr), func(t *testing.T) {
			reg := NewRegistry()
			// Preserve disjoint ranges and caller order rather than sorting or merging.
			want := []*EntryView{
				{Scope: EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(3)}},
				{SessionId: "other-session", Scope: EntryRange{
					SeqS: entry.SeqFromUint64(9), SeqE: entry.SeqFromUint64(11),
				}},
				{Scope: EntryRange{SeqS: entry.SeqFromUint64(5), SeqE: entry.SeqFromUint64(6)}},
			}
			ov := NewOverView(want, reg)
			if len(ov.Views) != len(want) || &ov.Views[0] != &want[0] {
				t.Fatal("NewOverView did not retain the view collection")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			payload, result := new(int), new(int)
			calls := 0
			if err := reg.Register(&testHandler{action: "batch", handle: func(gotCtx context.Context, got []*EntryView, p any) (any, error) {
				calls++
				if gotCtx != ctx || p != payload || len(got) != len(want) {
					t.Fatal("Handle did not forward context, payload and all views")
				}
				for i := range want {
					if got[i] != want[i] {
						t.Fatalf("view %d: got %p, want %p", i, got[i], want[i])
					}
				}
				return result, handlerErr
			}}); err != nil {
				t.Fatal(err)
			}
			got, err := ov.Handle(ctx, "batch", payload)
			if got != result || err != handlerErr || calls != 1 {
				t.Fatalf("Handle = %v, %v; calls = %d; want one handler invocation", got, err, calls)
			}
		})
	}
}
