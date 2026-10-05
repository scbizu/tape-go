# Tape behavior scenarios

Review the product behavior in [`features/storage.feature`](features/storage.feature),
[`features/agent.feature`](features/agent.feature), and
[`features/jev.feature`](features/jev.feature). Godog executes each Given / When / Then
step through `go test`; `_test.go` files contain the runner, steps and fixtures.
Scenarios invoke the public Tape and agent SDKs directly. No binary build is needed.

From the repository root (run `mise trust mise.toml` once for the new task file):

```sh
mise run e2e
# Or without mise:
go test -race ./e2e/... -timeout 120s
```

Four deterministic scenarios cover JSONL and bbolt persistence, close/reopen,
handoff and rewind through the ADK runner. Only the agent's model responses are
scripted; disk storage, session adaptation, tool dispatch and result delivery
execute normally. No API keys or network access are required.

For the two real JEV scenarios:

```sh
mise run e2e-live
# Or without mise:
TAPE_E2E_LIVE=1 go test -race ./e2e/... -run TestLiveJEVBehavior -count=1 -timeout 15m
```

The default suite excludes `@live` scenarios. Explicitly enabling them requires
`JEV_API_KEY`, `jev.api_key`, or `provider.jev.api_key` in the ignored
`e2e/config.toml`; missing credentials fail the live suite. With only a JEV key,
a deterministic projection summarizer is used. To use DeepSeek summaries, set
`DEEPSEEK_API_KEY`, `deepseek.api_key`, or `provider.deepseek.api_key`.
`DEEPSEEK_MODEL` optionally selects the model.

[The Lantern Road](testdata/tape_golden.json) supplies 100 original source entries
in 20 scenes and 20 queries with expected source entry IDs. Storage scenarios
verify source IDs, kinds, content and ordering before and after restart. JEV
scenarios additionally require at least 20 distinct complete-scene anchors,
unchanged anchors after restart, and all queries to recover their expected source
without unrelated scenes: 80 retrieval checks across both backends and phases.
Assertions do not compare generated summary wording.

Each scenario retains its temporary tape and `report.json`; `go test -v` prints
the artifact directories even on success. Reports include anchors, retrieval
results, expected/returned source IDs, timings and failures. Storage-only reports
do not claim search coverage. These BDDs replace the old `golden` manual commands.
The reusable `pkg/tape/testsuite` remains available to other consumers, but is not
run alongside the BDDs.

Interactive chat and the real-model rewind demo live in
[`examples/demo`](../examples/demo). They are examples, not automated acceptance
checks. Protocol and package boundary tests remain beside their packages.

To add behavior, write a scenario with observable outcomes in `features/`, then
register its steps. Each scenario has isolated state and a fresh disk directory;
cleanup closes storage even after failure. `mise run test` runs package tests and
the deterministic BDDs together (the nested e2e module is included explicitly).
