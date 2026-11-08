package view

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Handler defines an executable action over an ordered collection of EntryViews.
// Action must return a stable, non-empty identifier.
// Implementations are responsible for their own concurrency safety.
type Handler interface {
	Action() string
	Handle(ctx context.Context, views []*EntryView, payload any) (any, error)
}

// Registry resolves handlers by action name. Its zero value is ready for use.
// A Registry must not be copied after first use.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Register adds a handler without replacing an existing action.
func (r *Registry) Register(h Handler) error {
	if h == nil {
		return errors.New("view: nil handler")
	}
	action := h.Action()
	if action == "" {
		return errors.New("view: empty handler action")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.handlers[action]; ok {
		return fmt.Errorf("view: handler %q already registered", action)
	}
	if r.handlers == nil {
		r.handlers = make(map[string]Handler)
	}
	r.handlers[action] = h
	return nil
}

// Lookup returns the handler registered for action, if any.
func (r *Registry) Lookup(action string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[action]
	return h, ok
}

// OverView binds an EntryView to a handler registry.
type OverView struct {
	EntryView
	registry *Registry
}

// NewOverView wraps v without copying its entries. A nil registry is allowed;
// Handle reports an error when no registry is configured.
func NewOverView(v EntryView, reg *Registry) OverView {
	return OverView{EntryView: v, registry: reg}
}

// Handle dispatches action once with this view followed by any additional views.
// View pointers, their order, the caller's context and payload are preserved.
// Handler execution does not hold the registry lock.
func (ov *OverView) Handle(ctx context.Context, action string, payload any, additional ...*EntryView) (any, error) {
	if ov.registry == nil {
		return nil, errors.New("view: no handler registry")
	}
	h, ok := ov.registry.Lookup(action)
	if !ok {
		return nil, fmt.Errorf("view: handler %q not found", action)
	}
	views := make([]*EntryView, 1, 1+len(additional))
	views[0] = &ov.EntryView
	views = append(views, additional...)
	return h.Handle(ctx, views, payload)
}
