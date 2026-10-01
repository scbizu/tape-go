package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	jevext "github.com/scbizu/tape-go/pkg/ext/jev"
	"github.com/scbizu/tape-go/pkg/llm"
	jevprovider "github.com/scbizu/tape-go/pkg/provider/jev"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/testsuite"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

//go:embed testdata/tape_golden.json
var tapeGoldenJSON []byte

func runGoldenBackends(ctx context.Context, configure func(storageBackend, testsuite.Fixture) testsuite.Config) error {
	fixture, err := testsuite.ParseFixture(tapeGoldenJSON)
	if err != nil {
		return err
	}
	fmt.Printf("Tape golden: %s (%d source entries, %d windows, %d golden queries)\n", fixture.Title, fixture.SourceEntries, len(fixture.Windows()), len(fixture.Queries))
	var failures error
	for _, backend := range e2eBackends() {
		config := configure(backend, fixture)
		config.Progress = func(message string) { fmt.Println(message) }
		fmt.Printf("Golden scenario: %s\n", config.Name)
		dir, err := os.MkdirTemp("", "tape-go-golden-")
		if err != nil {
			return err
		}
		report, runErr := testsuite.Run(ctx, fixture, config, dir)
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0600)
		}
		runErr = errors.Join(runErr, err)
		fmt.Printf("golden report and tape: %s\n", dir)
		if runErr != nil {
			failures = errors.Join(failures, fmt.Errorf("%s: %w", config.Name, runErr))
			continue
		}
		fmt.Printf("Golden %s: OK\n", config.Name)
	}
	return failures
}

func runStorageGolden(ctx context.Context) error {
	return runGoldenBackends(ctx, func(backend storageBackend, _ testsuite.Fixture) testsuite.Config {
		return testsuite.Config{
			Name: "storage/" + backend.name,
			Open: func(ctx context.Context, dir string) (*tape.Tape, error) { return newTape(ctx, backend, dir) },
		}
	})
}

// Scene endings are checkpoint opportunities. JEV still makes the keep/skip
// decision for the complete view; intermediate source entries are not checkpoints.
type goldenSceneDecider struct {
	client *jevprovider.Client
	ends   map[string]bool
}

func (d goldenSceneDecider) ShouldAnchor(ctx context.Context, projection view.Projection) (bool, error) {
	if len(projection.Entries) == 0 || !d.ends[projection.Entries[len(projection.Entries)-1].Summary] {
		return false, nil
	}
	return d.client.ShouldAnchor(ctx, projection)
}

// JEV reports derivation errors separately from Store. The adapter makes those
// errors visible to the shared suite while keeping the original source entry.
type goldenJevStorage struct {
	*jevext.Storage
	anchorErr error
}

func (s *goldenJevStorage) Store(ctx context.Context, e entry.EntryLike) (entry.EntryLike, error) {
	stored, err := s.Storage.Store(ctx, e)
	return stored, errors.Join(err, s.anchorErr)
}

func jevGoldenConfig(backend storageBackend, fixture testsuite.Fixture, summarizer llm.Summarizer, client *jevprovider.Client) testsuite.Config {
	ends := make(map[string]bool)
	for _, window := range fixture.Windows() {
		ends[window.Entries[len(window.Entries)-1].Text] = true
	}
	return testsuite.Config{
		Name: "jev/" + backend.name, MinAnchors: 20,
		Open: func(ctx context.Context, dir string) (*tape.Tape, error) {
			base, err := backend.new(ctx, dir)
			if err != nil {
				return nil, err
			}
			t := &tape.Tape{OwnerID: ownerID}
			checked := &goldenJevStorage{}
			decorated, err := jevext.NewStorage(base, &t.View, jevext.Config{
				Decider: goldenSceneDecider{client: client, ends: ends}, Summarizer: summarizer, Classifier: client,
				OnError: func(err error) { checked.anchorErr = errors.Join(checked.anchorErr, err) },
			})
			if err != nil {
				t.TapeStorage = base
				t.Close()
				return nil, err
			}
			checked.Storage = decorated
			t.TapeStorage = checked
			if err := t.Init(ctx); err != nil {
				t.Close()
				return nil, err
			}
			return t, nil
		},
		Search: func(ctx context.Context, t *tape.Tape, query string) (view.EntryView, error) {
			return t.Find(ctx, query)
		},
		ValidateAnchors: validateJevGoldenAnchors,
	}
}

func validateJevGoldenAnchors(fixture testsuite.Fixture, seqs map[string]entry.Seq, anchors []testsuite.Anchor) error {
	windows := make(map[view.EntryRange]string)
	for _, window := range fixture.Windows() {
		windows[view.EntryRange{SeqS: seqs[window.Entries[0].ID], SeqE: seqs[window.Entries[len(window.Entries)-1].ID].Next()}] = window.ID
	}
	seen := make(map[string]bool)
	for _, anchor := range anchors {
		record, err := jevext.AnchorFromEntry(entry.NewEntry(entry.WithEntryID(anchor.Seq), entry.WithEntryKind(anchor.Kind), entry.WithEntryContent(anchor.Content)))
		if err != nil {
			return err
		}
		window, ok := windows[record.Scope]
		if !ok || record.Seq != record.Scope.SeqE || seen[window] {
			return fmt.Errorf("JEV anchor %s has an invalid or duplicate scene checkpoint", record.Seq)
		}
		seen[window] = true
	}
	return nil
}
