# View Handler Extension Design

## Problem and motivation

In `tape-go`, `view.EntryView` and `view.TapeView` are passive data representations of an entry sequence range `[SeqS, SeqE)`. Downstream systems (e.g., multi-agent collaboration frameworks and contextual tools) frequently need to execute operations directly coupled to a view:
- **Grant**: Issuing authorization leases, borrowing scopes, and validating consumer permissions.
- **Lock / Unlock**: Managing range concurrency, exclusive execution locks, and snapshot isolation.
- **Summary**: Condensing and projecting entries within a view scope into semantic summaries.
- **Sidecar / Crops**: Attaching out-of-band Z-axis records, audit entries, or annotations without consuming the main tape sequence or inflating context.

Currently, `tape-go` lacks a standard view handler binding mechanism, forcing downstream consumers to build bespoke wrappers and duplicate view abstractions.

One operation may need several disjoint entry ranges, such as summarizing
selected portions of a tape together. A handler receives all selected views in
one invocation so it can operate across ranges without filling gaps or losing
each view's metadata.

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

// Handler defines an executable action over an ordered collection of EntryViews.
type Handler interface {
	// Action returns the unique identifier for this handler.
	Action() string

	// Handle executes one operation against all provided views.
	Handle(ctx context.Context, views []*EntryView, payload any) (any, error)
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

// Handle executes action once against this view and any additional views.
func (ov *OverView) Handle(ctx context.Context, action string, payload any, additional ...*EntryView) (any, error)
```

`Handle` returns an error for a missing registry or unknown action. Otherwise it
passes the context and payload unchanged to the handler with an ordered slice
containing `&ov.EntryView` followed by `additional`. View pointers and their
order are preserved; dispatch does not merge, sort, deduplicate, or copy entry
data. The handler runs once for the entire collection, and its result and error
are returned unchanged. `NewOverView` keeps the provided registry pointer and
wraps the entry data without deep copying it.
Handlers and callers must coordinate any mutations to shared view data.

Existing calls to `ov.Handle(ctx, action, payload)` still dispatch a single view.
To operate on multiple views, use
`ov.Handle(ctx, action, payload, &other.EntryView)` or pass a slice with `views...`.
Handler implementations from `v0.2.0-anra.4` must change their `*EntryView`
parameter to `[]*EntryView`; this is an interface change. A single-view handler
can check the slice length and use `views[0]`. Direct calls to a resolved handler
also pass a slice. The handler determines which combinations of views its
operation accepts.

`Tape` exposes a `Registry *view.Registry` field, configured before use, and
provides a helper accessor:

```go
func (t *Tape) OverView(ctx context.Context, scope view.EntryRange) (view.OverView, error)
```

The helper continues to load one range per call through the existing storage
`Range(ctx, scope)` and wraps its result with `view.NewOverView`. The caller
supplies the owner scope through `ctx`, as
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
        OV --> EntryV["Own EntryView"]
        OV -->|One invocation: own view then additional views| Handler
    end

    subgraph CoreContract["Core Extension Contract (pkg/tape/view)"]
        Reg --> Handler["view.Handler (Handle receives []*EntryView)"]
    end

    subgraph Downstream["Downstream Implementations (e.g. Anra)"]
        Handler -.->|Implemented by| GrantH["GrantHandler"]
        Handler -.->|Implemented by| LockH["LockHandler"]
        Handler -.->|Implemented by| SummaryH["SummaryHandler"]
        Handler -.->|Implemented by| CropsH["CropsSidecarHandler"]

        App["Downstream App"] -->|ov.Handle(ctx, action, req, additional...)| OV
        CropsH -.->|Optional, explicitly configured| ExistingStore["Existing storage.TapeStorage"]
    end
```
