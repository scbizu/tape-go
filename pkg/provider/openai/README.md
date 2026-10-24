# OpenAI-compatible Chat Completions

This provider uses the official open-source
[OpenAI Go SDK](https://github.com/openai/openai-go) for API requests, errors,
streaming, and tool-call accumulation. `Model` translates ADK and Tape types and
implements `model.LLM`, `llm.Model`, and `llm.Summarizer`.

```go
import (
    "github.com/scbizu/tape-go/pkg/provider/openai"
    "github.com/openai/openai-go/v3/option"
)

m, err := openai.NewModel(apiKey, "gpt-4o-mini",
    option.WithBaseURL("https://api.openai.com/v1"),
)
if err != nil {
    return err
}
// Pass m to agent.NewADKRunner(m), or call GenerateContent directly.
```

An empty model name uses `DefaultModel`. The constructor accepts SDK
`option.RequestOption` values directly. Use `option.WithBaseURL` for compatible
services, `option.WithHTTPClient` for a custom HTTP client, and
`option.WithMaxRetries` to configure SDK retries. The provider defaults to a
two-minute request timeout and zero retries. API errors remain SDK `*openai.Error`
values, accessible with `errors.As` through provider error wrapping.

Supported operations include text, function tools, streaming, JSON object output,
chat-based reranking, and tape-view summaries. `MaxOutputTokens` maps to
`max_completion_tokens`; optional sampling settings are forwarded only when
supplied. Callers must select settings supported by their model.

Streaming yields partial text followed by a complete response containing text,
function calls, and usage. The SDK parses and accumulates stream events. Ending
iteration closes the stream. If no finish reason arrives, the provider returns
an incomplete-stream error.

This adapter uses Chat Completions. It does not implement the Responses API,
multimodal input, hosted tools, or embeddings. `Embedding` returns
`ErrEmbeddingUnsupported`. Summary and rerank operations require JSON object
output support. Response schemas and unrelated ADK settings are not translated.

See the [provider design](../../../docs/designs/openai-provider-design.md).
