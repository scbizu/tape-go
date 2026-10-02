package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/provider/ds"
	jevprovider "github.com/scbizu/tape-go/pkg/provider/jev"
	"github.com/scbizu/tape-go/pkg/tape/testsuite"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

func runJevE2E(ctx context.Context, deepSeekKey, jevKey string) error {
	var summarizer llm.Summarizer = projectionSummarizer{}
	if deepSeekKey != "" {
		model, err := ds.NewModel(deepSeekKey, os.Getenv("DEEPSEEK_MODEL"))
		if err != nil {
			return err
		}
		summarizer = model
		fmt.Printf("JEV summarizer: DeepSeek (%s)\n", model.Name())
	} else {
		fmt.Println("JEV summarizer: deterministic projection")
	}
	client, err := jevprovider.NewClient(jevKey)
	if err != nil {
		return err
	}
	return runGoldenBackends(ctx, func(backend storageBackend, fixture testsuite.Fixture) testsuite.Config {
		return jevGoldenConfig(backend, fixture, summarizer, client)
	})
}

// projectionSummarizer keeps the e2e scenario runnable with only a JEV key.
// The real JEV service still decides whether to anchor and scores retrieval.
type projectionSummarizer struct{}

func (projectionSummarizer) Summarize(_ context.Context, projection view.Projection) (llm.Summary, error) {
	var decisions []string
	for _, e := range projection.Entries {
		if text := strings.TrimSpace(e.Summary); text != "" {
			decisions = append(decisions, text)
		}
	}
	if len(decisions) == 0 {
		return llm.Summary{}, errors.New("JEV e2e: projection has no text to summarize")
	}
	return llm.Summary{Decisions: decisions}, nil
}

func jevAPIKey(path string) (string, error) {
	if key := os.Getenv("JEV_API_KEY"); key != "" {
		return key, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read JEV config %s: %w", path, err)
	}
	var config struct {
		Jev struct {
			APIKey string `toml:"api_key"`
		} `toml:"jev"`
		Provider struct {
			Jev struct {
				APIKey string `toml:"api_key"`
			} `toml:"jev"`
		} `toml:"provider"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse JEV config %s: %w", path, err)
	}
	if config.Provider.Jev.APIKey != "" {
		return config.Provider.Jev.APIKey, nil
	}
	if config.Jev.APIKey == "" {
		return "", fmt.Errorf("JEV_API_KEY, provider.jev.api_key, or jev.api_key in %s is required", path)
	}
	return config.Jev.APIKey, nil
}
