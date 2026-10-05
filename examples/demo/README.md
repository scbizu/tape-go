# Interactive Tape demo

From the repository root:

```sh
go run ./examples/demo chat jsonl
go run ./examples/demo chat bbolt
go run ./examples/demo toolcall
```

These commands use a real DeepSeek model. Provide `DEEPSEEK_API_KEY`, or
`deepseek.api_key` / `provider.deepseek.api_key` in the ignored `e2e/config.toml`.
`DEEPSEEK_MODEL` optionally selects the model. Chat exits with `/exit` or Ctrl-C.
Temporary tapes are removed on exit.

Automated behavior acceptance checks are in [`e2e`](../../e2e/README.md).
