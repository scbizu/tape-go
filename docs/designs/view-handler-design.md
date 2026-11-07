# View Handler Extension Design

## Problem and motivation

In `tape-go`, `view.EntryView` and `view.TapeView` are passive data representations of an entry sequence range `[SeqS, SeqE)`. Downstream systems (e.g., multi-agent collaboration frameworks and contextual tools) frequently need to execute operations directly coupled to a view:
- **Grant**: Issuing authorization leases, borrowing scopes, and validating consumer permissions.
- **Lock / Unlock**: Managing range concurrency, exclusive execution locks, and snapshot isolation.
- **Summary**: Condensing and projecting entries within a view scope into semantic summaries.
- **Sidecar / Crops**: Attaching out-of-band Z-axis records, audit entries, or annotations without consuming the main tape sequence or inflating context.

Currently, `tape-go` lacks a standard view handler binding mechanism, forcing downstream consumers to build bespoke wrappers and duplicate view abstractions.

## Architecture and invariants

1. **Passive view data vs. active OverView**: `view.EntryView` and `view.TapeView` remain lightweight, passive data structures. Behavioral operations are exposed via `view.OverView` (capital `V` denoting an active, bird's-eye capability extension over `View`) and resolved through `view.Registry`.
2. **Generic extension protocol**: `tape-go` specifies only the general dispatch mechanism (`Handler`, `Registry`, and `OverView.Handle`). It does not bind or hardcode domain-specific handler schemas (such as grant, lock, or summary payloads), leaving concrete implementations to downstream applications or extension packages.
3. **Reuse existing storage**: This mechanism creates no databases, files, buckets, or persistent handler state. Any downstream handler persistence must reuse existing abstractions and explicitly configured storage; provisioning additional storage requires a separate, explicit instruction. Dispatch itself never appends domain metadata to the main tape sequence.
4. **Stateless registry**: `view.Registry` is a stateless capability dispatcher; stateful or persistent logic is managed internally by individual handlers or external stores.
5. **Context and identity propagation**: All operations pass standard `context.Context`, preserving owner scope, caller identity, timeouts, and cancellations.

## Specification

### 1. Handler contract

```go
package view

import "context"

// Handler defines an executable action bound to an EntryView.
type Handler interface {
	// Action returns the unique identifier for this handler.
	Action() string

	// Handle executes the handler operation against the provided EntryView.
	Handle(ctx context.Context, v *EntryView, payload any) (any, error)
}
```

### 2. Registry and OverView

A `Registry` registers and resolves handlers by action name:

```go
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

func NewRegistry() *Registry
func (r *Registry) Register(h Handler) error
func (r *Registry) Lookup(action string) (Handler, bool)
```

Registration and lookup are safe to call concurrently. The zero value is usable;
do not copy a registry after first use. `Register` rejects a nil handler, an empty
action, and duplicate actions without replacing the original handler. Actions
are exact, case-sensitive names and must remain stable. The registry lock covers
only map access, never handler execution. Handler implementations are responsible
for their own concurrency safety.

`OverView` decorates `EntryView` with registered handlers:

```go
type OverView struct {
	EntryView
	registry *Registry
}

func NewOverView(v EntryView, reg *Registry) OverView

// Handle executes the registered handler matching action against this view.
func (ov *OverView) Handle(ctx context.Context, action string, payload any) (any, error)
```

`Handle` returns an error for a missing registry or unknown action. Otherwise it
passes the context and payload unchanged to the handler with `&ov.EntryView`,
and returns the handler's result and error unchanged. `NewOverView` keeps the
provided registry pointer and wraps the entry data without deep copying it.
Handlers and callers must coordinate any mutations to shared view data.

`Tape` exposes a `Registry *view.Registry` field, configured before use, and
provides a helper accessor:

```go
func (t *Tape) OverView(ctx context.Context, scope view.EntryRange) (view.OverView, error)
```

The helper calls the existing storage `Range(ctx, scope)` and wraps its result
with `view.NewOverView`. The caller supplies the owner scope through `ctx`, as
with `Range` and `Find`; `OwnerID` does not replace that context. Nil storage
returns an error, and range errors propagate unchanged. The helper does not
alter `Tape.View` or the I/O read position. A nil registry permits loading the
view, but dispatch then returns an error. No global registry is introduced.

## Design diagram

```mermaid
flowchart TD
    subgraph TapeGo["tape-go (pkg/tape)"]
        Tape["Tape (io.ReadWriteCloser)"]
        Storage["TapeStorage"]
        Tape --> Storage
        Tape -->|Registry field| Reg["view.Registry (RWMutex + handlers)"]
        Tape -->|Range then NewOverView| OV["view.OverView"]
        OV --> Reg
        OV --> EntryV["view.EntryView (Passive Data)"]
    end

    subgraph CoreContract["Core Extension Contract (pkg/tape/view)"]
        Reg --> Handler["view.Handler (Action / Handle)"]
    end

    subgraph Downstream["Downstream Implementations (e.g. Anra)"]
        Handler -.->|Implemented by| GrantH["GrantHandler"]
        Handler -.->|Implemented by| LockH["LockHandler"]
        Handler -.->|Implemented by| SummaryH["SummaryHandler"]
        Handler -.->|Implemented by| CropsH["CropsSidecarHandler"]

        App["Downstream App"] -->|ov.Handle(ctx, action, req)| OV
        CropsH -.->|Optional, explicitly configured| ExistingStore["Existing storage.TapeStorage"]
    end
```
