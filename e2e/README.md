# Manual tape golden scenarios

Run the provider-independent storage scenario from the repository root. It needs no API keys:

```sh
go run ./e2e golden storage
```

Run the JEV retrieval scenario with local provider credentials:

```sh
go run ./e2e golden jev
```

`go run ./e2e jev` remains an alias. The JEV key can be set via `JEV_API_KEY`, `jev.api_key`, or `provider.jev.api_key` in the ignored `e2e/config.toml`. With only a JEV key, the scenario uses a deterministic projection summary. For DeepSeek summaries, provide `DEEPSEEK_API_KEY`, `deepseek.api_key`, or `provider.deepseek.api_key`; `DEEPSEEK_MODEL` optionally selects the model. Summary and reranking requests omit `max_tokens` and use the provider's defaults. Generation requests still honor an explicitly supplied `MaxOutputTokens`.

Both scenarios use [The Lantern Road](testdata/tape_golden.json), an original fantasy expedition told in 100 source entries across 20 five-entry scenes. Recurring characters encounter different passwords, fares, bells, routes, supplies, and promises. Its 20 golden queries specify expected source entry IDs. The fixture contains source data and expectations, without a JEV-specific anchor requirement.

The shared runner is [`pkg/tape/testsuite`](../pkg/tape/testsuite). It accepts fixtures of arbitrary size with contiguous windows of variable length. For each backend, it writes the fixture, verifies unchanged source IDs, kinds, content, and order, then closes and reopens the tape and repeats persistence validation. Derived anchor IDs, kinds, and contents must survive unchanged. When a search adapter is supplied, every golden query runs before and after restart; its result must contain the expected source entry and exclude unrelated windows. Golden values are source IDs, rather than exact generated summary text.

The storage scenario exercises JSONL and bbolt persistence with the same 100-entry fixture. It does not require anchors or execute semantic searches, and its report explicitly marks search coverage as disabled.

The JEV scenario supplies a checkpoint policy, anchor validator, and `Tape.Find` search adapter to that shared runner:

- Each scene ending is a checkpoint opportunity. JEV makes the real keep/skip decision for the complete scene, and the configured summarizer builds its memory state.
- At least 20 JEV anchors must be generated through the storage decorator. Each must cover one distinct bounded scene. With the 100 source entries, this produces at least 120 persisted entries.
- All 20 queries must pass both before and after restart on JSONL and bbolt: 80 retrieval checks in total.

Each backend retains its temporary tape and a `report.json` with anchor contents, search coverage, expected and returned source IDs, timings, and failures. The command prints progress, phase totals, and the artifact directory. Any storage, anchor, or retrieval failure causes a nonzero exit status. These remain manual commands; there are no e2e `*_test.go` files.

To add another provider or storage implementation, reuse `testsuite.ParseFixture` and `testsuite.Run` with a `testsuite.Config`. Supply an `Open` function that initializes and reopens the same tape directory. Optionally supply `Search`, `MinAnchors`, and `ValidateAnchors` for the capabilities under test. `Progress` receives log messages, and the returned `Report` can be saved by the caller. Storage-only configurations omit `Search` and do not claim query coverage.
