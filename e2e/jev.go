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
	fixture, err := loadJevGolden()
	if err != nil {
		return err
	}
	fmt.Printf("JEV golden: %s (%d source entries, at least %d anchors, %d queries per phase)\n", fixture.Title, fixture.SourceEntries, fixture.MinAnchors, len(fixture.Queries))
	var failures error
	for _, backend := range e2eBackends() {
		fmt.Printf("JEV backend: %s\n", backend.name)
		dir, err := os.MkdirTemp("", "tape-go-jev-e2e-")
		if err != nil {
			return err
		}
		err = runJevScenario(ctx, backend, dir, summarizer, client)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("%s backend: %w", backend.name, err))
			continue
		}
		fmt.Println("JEV golden tape, retrieval, and restart: OK")
	}
	return failures
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
