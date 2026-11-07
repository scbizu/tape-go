# View Handler Extension Design

## Problem and motivation

In `tape-go`, `view.EntryView` and `view.TapeView` are passive data representations of an entry sequence range `[SeqS, SeqE)`. Downstream systems (e.g., multi-agent collaboration frameworks and contextual tools) frequently need to execute operations directly coupled to a view:
- **Grant**: Issuing authorization leases, borrowing scopes, and validating consumer permissions.
- **Lock / Unlock**: Managing range concurrency, exclusive execution locks, and snapshot isolation.
- **Summary**: Condensing and projecting entries within a view scope into semantic summaries.
- **Sidecar / Crops**: Attaching out-of-band Z-axis records, audit entries, or annotations without consuming the main tape sequence or inflating context.

Currently, `tape-go` lacks a standard view handler binding mechanism, forcing downstream consumers to build bespoke wrappers and duplicate view abstractions.

## Architecture and invariants

1. **Passive view data vs. active OverView**: `view.EntryView` and `view.TapeView` remain lightweight, immutable data structures. Behavioral operations are exposed via `view.OverView` (capital `V` denoting an active, bird's-eye capability extension over `View`) and resolved through `view.Registry`.
2. **Generic extension protocol**: `tape-go` specifies only the general dispatch mechanism (`Handler`, `Registry`, and `OverView.Handle`). It does not bind or hardcode domain-specific handler schemas (such as grant, lock, or summary payloads), leaving concrete implementations to downstream applications or extension packages.
3. **Storage abstraction reuse with instance isolation**: Handlers that persist data (such as Crops / Side-Tape) reuse the `storage.TapeStorage` interface and append-only invariants, but MUST target isolated storage instances (e.g., a separate `.crops.db` or dedicated bucket). This prevents domain metadata from polluting main tape entry sequences (`entry.Seq`) and LLM context windows.
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
	handlers map[string]Handler
}

func NewRegistry() *Registry
func (r *Registry) Register(h Handler) error
func (r *Registry) Lookup(action string) (Handler, bool)
```

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

`Tape` provides helper accessors:

```go
func (t *Tape) OverView(ctx context.Context, scope EntryRange) (OverView, error)
```

## Design diagram

```mermaid
flowchart TD
    subgraph TapeGo["tape-go (pkg/tape)"]
        Tape["Tape (io.ReadWriteCloser)"]
        Storage["TapeStorage"]
        Tape --> Storage
        Tape --> Reg["view.Registry"]
        Tape --> OV["view.OverView"]
        OV --> EntryV["view.EntryView (Immutable Data)"]
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
        CropsH --> CropsStore["Side-Tape Storage (Isolated instance)"]
    end
```
