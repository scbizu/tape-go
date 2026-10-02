package llm

import "context"

type Model interface {
	IsEnable() bool
	// MaxTokenLimit returns the model's advertised maximum output token count.
	// Zero means the limit is unknown; callers should use the provider's defaults.
	MaxTokenLimit(context.Context) (int, error)
	// Embedding encodes the given text into a vector representation (embedding) and returns it as a slice of float32 values.
	Embedding(
		context.Context,
		string,
	) ([]float32, error)
	// ReRank takes
	// - a context
	// - a query string(prompt)
	// - a slice of candidate strings
	// returns a reordered slice of candidate strings based on their relevance to the query
	// along with any error encountered during the re-ranking process.
	ReRank(
		context.Context,
		string,
		[]string,
	) ([]string, error)
}

type modelKey struct{}

func WithModel(ctx context.Context, model Model) context.Context {
	return context.WithValue(ctx, modelKey{}, model)
}

func ModelFromContext(ctx context.Context) (Model, bool) {
	model, ok := ctx.Value(modelKey{}).(Model)
	return model, ok
}
