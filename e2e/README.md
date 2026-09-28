# E2E demos

Run the JEV integration scenario from the repository root:

```sh
JEV_API_KEY=... go run ./e2e jev
```

The JEV key can also be set as `jev.api_key` or `provider.jev.api_key` in the ignored `e2e/config.toml`. With only a JEV key, the scenario uses a deterministic summary of the stored fact. To test DeepSeek summarization as well, provide `DEEPSEEK_API_KEY` or `provider.deepseek.api_key`; `DEEPSEEK_MODEL` optionally selects the model. The scenario runs against both JSONL and bbolt using temporary storage. For each backend it writes a durable fact, checks that JEV creates an anchor, searches for the fact, then closes and reopens the tape to check that search still works.

The deterministic local integration test does not need API keys:

```sh
go test ./e2e
```
