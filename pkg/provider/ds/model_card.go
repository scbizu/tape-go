package ds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	deepseek "github.com/cohesion-org/deepseek-go"
	"github.com/cohesion-org/deepseek-go/utils"
)

// modelCardClient preserves /models metadata that the SDK's Model type drops.
// Successful discovery is cached for this client; transient failures are retried
// on the next call. Endpoints without output metadata use their server defaults.
type modelCardClient struct {
	*deepseek.Client
	mu     sync.Mutex
	limits map[string]int
}

func (c *modelCardClient) OutputLimit(ctx context.Context, name string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if c.limits != nil {
		return c.limits[name], nil
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	req, err := utils.NewRequestBuilder(c.AuthToken).
		SetBaseURL(strings.TrimRight(c.BaseURL, "/") + "/").SetPath("models").BuildGet(ctx)
	if err != nil {
		return 0, fmt.Errorf("ds: model card request: %w", err)
	}
	resp, err := deepseek.HandleNormalRequest(*c.Client, req)
	if err != nil {
		return 0, fmt.Errorf("ds: model card request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented {
		c.limits = make(map[string]int)
		return 0, nil
	}
	if resp.StatusCode >= 400 {
		return 0, fmt.Errorf("ds: model cards: %w", deepseek.HandleAPIError(resp))
	}
	var cards struct {
		Data []struct {
			ID              string `json:"id"`
			MaxOutputTokens int    `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		return 0, fmt.Errorf("ds: decode model cards: %w", err)
	}
	limits := make(map[string]int, len(cards.Data))
	for _, card := range cards.Data {
		if card.MaxOutputTokens < 0 {
			return 0, fmt.Errorf("ds: model %q has a negative output limit", card.ID)
		}
		limits[card.ID] = card.MaxOutputTokens
	}
	c.limits = limits
	return limits[name], nil
}
