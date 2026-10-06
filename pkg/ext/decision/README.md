# Decision extension

`github.com/scbizu/tape-go/pkg/ext/decision` adds decision-based checkpointing
and retrieval to any `storage.TapeStorage`. The extension owns anchor persistence,
snapshots, and best-result selection; vendor adapters own API requests,
authentication, retries, and response translation.

Configure the capabilities independently through `decision.Config`:

- `Decider` implements `AnchorDecider.ShouldAnchor` for the complete active-view
  projection and returns whether it contains durable information.
- `Summarizer` implements `llm.Summarizer` and produces a nonempty `llm.Summary`.
  It can use a different vendor from the decision provider.
- `Classifier` implements `Classifier.Classify` and scores anchor memory states
  against a query. Results use input candidate indexes, with higher scores ranking
  first and confidence breaking ties. Scores must be comparable within the call;
  no vendor-specific score range is required. Results may arrive in any order.
  Yield errors for failed requests or invalid responses and stop when the consumer
  returns false. An empty result sequence means no match.
- `OnError` receives checkpoint derivation failures after a source entry has been
  stored. Such failures do not undo the source write.

For example, the existing `pkg/provider/jev.Client` implements both decision
interfaces and can be combined with any summarizer:

```go
decorated, err := decision.NewStorage(base, &t.View, decision.Config{
    Decider:    client,
    Summarizer: summarizer,
    Classifier: client,
    OnError:    onError,
})
if err != nil {
    return err
}
t.TapeStorage = decorated
if err := t.Init(ctx); err != nil {
    return err
}
```

To integrate another vendor, add an adapter under `pkg/provider/<vendor>` that
implements either or both interfaces and inject it into `Config`. Keep its request
and response types in that provider package; the extension does not import vendor
packages. No vendor registry or changes to the storage/finder are needed.

Anchors use the vendor-neutral entry kind `anchor:decision` and the `Anchor` JSON
payload.
