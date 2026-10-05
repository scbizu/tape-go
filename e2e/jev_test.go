package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

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

func optionalDeepSeekAPIKey(path string) (string, error) {
	if apiKey := os.Getenv("DEEPSEEK_API_KEY"); apiKey != "" {
		return apiKey, nil
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read DeepSeek config %s: %w", path, err)
	}
	var config struct {
		DeepSeek struct {
			APIKey string `toml:"api_key"`
		} `toml:"deepseek"`
		Provider struct {
			DeepSeek struct {
				APIKey string `toml:"api_key"`
			} `toml:"deepseek"`
		} `toml:"provider"`
	}
	if err := toml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse DeepSeek config %s: %w", path, err)
	}
	if config.Provider.DeepSeek.APIKey != "" {
		return config.Provider.DeepSeek.APIKey, nil
	}
	return config.DeepSeek.APIKey, nil
}
