# E2E demos

Run the JEV integration scenario from the repository root:

```sh
JEV_API_KEY=... go run ./e2e jev
```

The JEV key can also be set as `jev.api_key` or `provider.jev.api_key` in the ignored `e2e/config.toml`. With only a JEV key, the scenario uses a deterministic projection summary. To use DeepSeek summarization, provide `DEEPSEEK_API_KEY`, `deepseek.api_key`, or `provider.deepseek.api_key`; `DEEPSEEK_MODEL` optionally selects the model.

The manual golden experiment uses [The Lantern Road](testdata/jev_golden.json), an original fantasy expedition told in 100 source entries across 20 five-entry scenes. Recurring characters encounter different passwords, fares, bells, routes, supplies, and promises. The fixture includes 20 golden queries with explicit expected source entry IDs, covering the beginning, middle, and end of the tape.

For each of JSONL and bbolt, the experiment:

- Gives JEV one checkpoint opportunity at the end of each scene. JEV decides whether to keep the complete scene, and the configured summarizer creates its memory state. Anchors are generated through the storage decorator, rather than inserted as fixtures.
- Requires 100 unchanged source entries and at least 20 persisted JEV anchors. Each anchor must cover its own bounded scene. This means at least 120 persisted entries including the derived anchors.
- Runs all 20 queries against the full anchor collection. Each selected view must include its expected source entry and must not contain entries from another scene. The golden values are source IDs, rather than exact generated summary text.
- Closes and reopens the tape, verifies that source entries and anchor IDs, states, and scopes survive unchanged, then repeats every query. All 80 retrieval checks across both backends and phases must pass.

The command prints ingestion progress, individual query results, and phase totals. Each backend retains its temporary tape and a `report.json` containing generated anchor states, expected and returned source IDs, timings, and failures; the directory path is printed. A missing anchor, storage error, or incorrect retrieval causes a nonzero exit status. No Go `*_test.go` file is needed for this manual experiment.
