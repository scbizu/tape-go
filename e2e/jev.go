package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"

	jevext "github.com/scbizu/tape-go/pkg/ext/jev"
	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/provider/ds"
	jevprovider "github.com/scbizu/tape-go/pkg/provider/jev"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const (
	jevFact  = "Project Atlas uses ap-southeast-1 as its deployment region."
	jevQuery = "What deployment region does Project Atlas use?"
)

func runJevE2E(ctx context.Context, deepSeekKey, jevKey string) error {
	var summarizer llm.Summarizer = projectionSummarizer{}
	if deepSeekKey != "" {
		model, err := ds.NewModel(deepSeekKey, os.Getenv("DEEPSEEK_MODEL"))
		if err != nil {
			return err
		}
		summarizer = model
	}
	client, err := jevprovider.NewClient(jevKey)
	if err != nil {
		return err
	}
	for _, backend := range e2eBackends() {
		fmt.Printf("JEV backend: %s\n", backend.name)
		dir, err := os.MkdirTemp("", "tape-go-jev-e2e-")
		if err != nil {
			return err
		}
		err = runJevScenario(ctx, backend, dir, summarizer, client)
		os.RemoveAll(dir)
		if err != nil {
			return fmt.Errorf("%s backend: %w", backend.name, err)
		}
		fmt.Println("JEV anchor, retrieval, and restart: OK")
	}
	return nil
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

func runJevScenario(ctx context.Context, backend storageBackend, dir string, summarizer llm.Summarizer, client *jevprovider.Client) error {
	var anchorErr error
	newTape := func() (*tape.Tape, error) {
		base, err := backend.new(ctx, dir)
		if err != nil {
			return nil, err
		}
		t := &tape.Tape{OwnerID: ownerID}
		decorated, err := jevext.NewStorage(base, &t.View, jevext.Config{
			Decider: client, Summarizer: summarizer, Classifier: client,
			OnError: func(err error) { anchorErr = errors.Join(anchorErr, err) },
		})
		if err != nil {
			return nil, err
		}
		t.TapeStorage = decorated
		if err := t.Init(ctx); err != nil {
			t.Close()
			return nil, err
		}
		return t, nil
	}

	t, err := newTape()
	if err != nil {
		return err
	}
	defer func() {
		if t != nil {
			t.Close()
		}
	}()
	stored, err := t.Store(ctx, entry.NewEntry(
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent(jevFact),
	))
	if err != nil {
		return fmt.Errorf("store fact: %w", err)
	}
	if anchorErr != nil {
		return fmt.Errorf("create JEV anchor: %w", anchorErr)
	}
	if stored == nil || stored.GetID().IsZero() {
		return errors.New("stored fact has no ID")
	}
	anchorCount := 0
	for anchor, err := range t.Anchors(ctx) {
		if err != nil {
			return fmt.Errorf("read anchors: %w", err)
		}
		if anchor.GetKind() != jevext.Kind {
			continue
		}
		record, err := jevext.AnchorFromEntry(anchor)
		if err != nil {
			return err
		}
		if record.State.IsZero() || record.Scope.SeqS != stored.GetID() || record.Scope.SeqE != stored.GetID().Next() {
			return fmt.Errorf("JEV anchor has unexpected state or scope: %+v", record)
		}
		anchorCount++
	}
	if anchorCount != 1 {
		return fmt.Errorf("expected one JEV anchor, got %d", anchorCount)
	}
	if err := checkJevRetrieval(ctx, t); err != nil {
		return fmt.Errorf("search before restart: %w", err)
	}
	if err := t.Close(); err != nil {
		return fmt.Errorf("close tape: %w", err)
	}
	t = nil

	t, err = newTape()
	if err != nil {
		return fmt.Errorf("reopen tape: %w", err)
	}
	if err := checkJevRetrieval(ctx, t); err != nil {
		return fmt.Errorf("search after restart: %w", err)
	}
	return nil
}

func checkJevRetrieval(ctx context.Context, t *tape.Tape) error {
	found, err := t.Find(ctx, jevQuery)
	if err != nil {
		return err
	}
	for _, e := range found.Raw {
		if e.GetKind() == entry.EntryUser && strings.Contains(e.GetSummary(), jevFact) {
			return nil
		}
	}
	return fmt.Errorf("JEV search did not return the stored fact: %+v", found.Scope)
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
