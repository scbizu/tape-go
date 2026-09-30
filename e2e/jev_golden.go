package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	jevext "github.com/scbizu/tape-go/pkg/ext/jev"
	"github.com/scbizu/tape-go/pkg/llm"
	jevprovider "github.com/scbizu/tape-go/pkg/provider/jev"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

//go:embed testdata/jev_golden.json
var jevGoldenJSON []byte

const goldenSceneSize = 5

type goldenEntry struct {
	ID     string          `json:"id"`
	Window string          `json:"window"`
	Kind   entry.EntryKind `json:"kind"`
	Text   string          `json:"text"`
}

type goldenQuery struct {
	ID              string `json:"id"`
	Query           string `json:"query"`
	ExpectedEntryID string `json:"expected_entry_id"`
}

type goldenFixture struct {
	Title         string        `json:"title"`
	SourceEntries int           `json:"source_entries"`
	MinAnchors    int           `json:"min_anchors"`
	Entries       []goldenEntry `json:"entries"`
	Queries       []goldenQuery `json:"queries"`
}

func loadJevGolden() (goldenFixture, error) {
	var fixture goldenFixture
	if err := json.Unmarshal(jevGoldenJSON, &fixture); err != nil {
		return fixture, err
	}
	if fixture.SourceEntries != 100 || len(fixture.Entries) != fixture.SourceEntries || fixture.MinAnchors < 20 {
		return fixture, errors.New("JEV golden fixture must have 100 source entries and require at least 20 anchors")
	}
	entries := make(map[string]int)
	windows := make(map[string]bool)
	for i, e := range fixture.Entries {
		if e.ID == "" || e.Window == "" || e.Text == "" || (e.Kind != entry.EntryUser && e.Kind != entry.EntryAssistant) {
			return fixture, fmt.Errorf("invalid golden entry %d", i+1)
		}
		if _, exists := entries[e.ID]; exists {
			return fixture, fmt.Errorf("duplicate golden entry %s", e.ID)
		}
		entries[e.ID] = i
		if i%goldenSceneSize == 0 {
			if windows[e.Window] {
				return fixture, fmt.Errorf("noncontiguous scene %s", e.Window)
			}
			windows[e.Window] = true
		} else if e.Window != fixture.Entries[i-1].Window {
			return fixture, fmt.Errorf("scene %s must contain five entries", e.Window)
		}
	}
	queries := make(map[string]bool)
	covered := make(map[string]bool)
	for _, q := range fixture.Queries {
		i, exists := entries[q.ExpectedEntryID]
		if !exists || i%goldenSceneSize != goldenSceneSize-1 || q.ID == "" || q.Query == "" || queries[q.ID] {
			return fixture, fmt.Errorf("invalid golden query %s", q.ID)
		}
		queries[q.ID] = true
		covered[fixture.Entries[i].Window] = true
	}
	if len(covered) != len(windows) {
		return fixture, errors.New("golden queries must cover every scene")
	}
	return fixture, nil
}

// Scene endings are checkpoint opportunities, as in a caller that checkpoints
// at turn boundaries. JEV still makes the keep/skip decision for the whole scene.
type goldenSceneDecider struct {
	client *jevprovider.Client
}

func (d goldenSceneDecider) ShouldAnchor(ctx context.Context, projection view.Projection) (bool, error) {
	if len(projection.Entries) != goldenSceneSize {
		return false, nil
	}
	return d.client.ShouldAnchor(ctx, projection)
}

type goldenQueryResult struct {
	ID            string   `json:"id"`
	Phase         string   `json:"phase"`
	Query         string   `json:"query"`
	ExpectedEntry string   `json:"expected_entry"`
	ReturnedIDs   []string `json:"returned_ids"`
	Passed        bool     `json:"passed"`
	DurationMS    int64    `json:"duration_ms"`
	Error         string   `json:"error,omitempty"`
}

type goldenReport struct {
	Title         string                `json:"title"`
	Backend       string                `json:"backend"`
	SourceEntries int                   `json:"source_entries"`
	MinAnchors    int                   `json:"min_anchors"`
	Anchors       []jevext.AnchorRecord `json:"anchors"`
	Results       []goldenQueryResult   `json:"results"`
	DurationMS    int64                 `json:"duration_ms"`
	Error         string                `json:"error,omitempty"`
}

func runJevScenario(ctx context.Context, backend storageBackend, dir string, summarizer llm.Summarizer, client *jevprovider.Client) (runErr error) {
	fixture, err := loadJevGolden()
	if err != nil {
		return err
	}
	started := time.Now()
	report := goldenReport{Title: fixture.Title, Backend: backend.name, MinAnchors: fixture.MinAnchors}
	defer func() {
		report.DurationMS = time.Since(started).Milliseconds()
		if runErr != nil {
			report.Error = runErr.Error()
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("write golden report: %w", err))
		}
		fmt.Printf("golden report and tape: %s\n", dir)
	}()

	var anchorErr error
	openTape := func() (*tape.Tape, error) {
		base, err := backend.new(ctx, dir)
		if err != nil {
			return nil, err
		}
		t := &tape.Tape{OwnerID: ownerID}
		decorated, err := jevext.NewStorage(base, &t.View, jevext.Config{
			Decider: goldenSceneDecider{client}, Summarizer: summarizer, Classifier: client,
			OnError: func(err error) { anchorErr = errors.Join(anchorErr, err) },
		})
		if err != nil {
			t.TapeStorage = base
			t.Close()
			return nil, err
		}
		t.TapeStorage = decorated
		if err := t.Init(ctx); err != nil {
			t.Close()
			return nil, err
		}
		return t, nil
	}

	t, err := openTape()
	if err != nil {
		return err
	}
	defer func() {
		if t != nil {
			runErr = errors.Join(runErr, t.Close())
		}
	}()
	seqs := make(map[string]entry.Seq)
	for i, e := range fixture.Entries {
		if i%goldenSceneSize == 0 {
			meta, err := t.Get(ctx)
			if err != nil {
				return err
			}
			t.SetView(view.EntryRange{SeqS: meta.Scope.SeqE.Next()})
		}
		stored, err := t.Store(ctx, entry.NewEntry(entry.WithEntryKind(e.Kind), entry.WithEntryContent(e.Text)))
		if err != nil {
			return fmt.Errorf("store %s: %w", e.ID, err)
		}
		if anchorErr != nil {
			return fmt.Errorf("anchor %s: %w", e.Window, anchorErr)
		}
		if stored == nil || stored.GetID().IsZero() {
			return fmt.Errorf("stored %s has no ID", e.ID)
		}
		seqs[e.ID] = stored.GetID()
		report.SourceEntries++
		if (i+1)%goldenSceneSize == 0 {
			fmt.Printf("  stored %d/%d source entries (%s)\n", i+1, fixture.SourceEntries, e.Window)
		}
	}
	report.Anchors, err = validateGoldenTape(ctx, t, fixture, seqs)
	if err != nil {
		return err
	}
	fmt.Printf("  verified %d source entries and %d JEV anchors\n", report.SourceEntries, len(report.Anchors))
	beforeErr := checkGoldenRetrieval(ctx, t, fixture, seqs, "before restart", &report)
	if err := t.Close(); err != nil {
		return errors.Join(beforeErr, err)
	}
	t = nil
	t, err = openTape()
	if err != nil {
		return errors.Join(beforeErr, fmt.Errorf("reopen: %w", err))
	}
	restored, err := validateGoldenTape(ctx, t, fixture, seqs)
	if err != nil {
		return errors.Join(beforeErr, fmt.Errorf("validate restored tape: %w", err))
	}
	if !reflect.DeepEqual(report.Anchors, restored) {
		return errors.Join(beforeErr, errors.New("anchor IDs, states, or scopes changed after restart"))
	}
	return errors.Join(beforeErr, checkGoldenRetrieval(ctx, t, fixture, seqs, "after restart", &report))
}

func validateGoldenTape(ctx context.Context, t *tape.Tape, fixture goldenFixture, seqs map[string]entry.Seq) ([]jevext.AnchorRecord, error) {
	meta, err := t.Get(ctx)
	if err != nil {
		return nil, err
	}
	all, err := t.Range(ctx, view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: meta.Scope.SeqE.Next()})
	if err != nil {
		return nil, err
	}
	var anchors []jevext.AnchorRecord
	var sourceIndex int
	for _, e := range all.Raw {
		if e == nil {
			return anchors, errors.New("tape contains a nil entry")
		}
		if e.GetKind().IsAnchor() {
			record, err := jevext.AnchorFromEntry(e)
			if err != nil {
				return anchors, err
			}
			if sourceIndex == 0 || sourceIndex%goldenSceneSize != 0 {
				return anchors, errors.New("anchor was created outside a scene checkpoint")
			}
			first := fixture.Entries[sourceIndex-goldenSceneSize]
			last := fixture.Entries[sourceIndex-1]
			if record.Scope.SeqS != seqs[first.ID] || record.Scope.SeqE != seqs[last.ID].Next() {
				return anchors, fmt.Errorf("anchor %s covers the wrong scene", record.Seq)
			}
			anchors = append(anchors, record)
			continue
		}
		if sourceIndex >= len(fixture.Entries) {
			return anchors, errors.New("unexpected extra source entry")
		}
		want := fixture.Entries[sourceIndex]
		if e.GetID() != seqs[want.ID] || e.GetKind() != want.Kind || e.GetSummary() != want.Text {
			return anchors, fmt.Errorf("source entry %s changed", want.ID)
		}
		sourceIndex++
	}
	if sourceIndex != fixture.SourceEntries || len(anchors) < fixture.MinAnchors {
		return anchors, fmt.Errorf("golden tape has %d/%d source entries and %d/%d required JEV anchors", sourceIndex, fixture.SourceEntries, len(anchors), fixture.MinAnchors)
	}
	return anchors, nil
}

func checkGoldenRetrieval(ctx context.Context, t *tape.Tape, fixture goldenFixture, seqs map[string]entry.Seq, phase string, report *goldenReport) error {
	byID := make(map[string]goldenEntry)
	bySeq := make(map[entry.Seq]goldenEntry)
	for _, e := range fixture.Entries {
		byID[e.ID] = e
		bySeq[seqs[e.ID]] = e
	}
	var failures error
	passed := 0
	for _, q := range fixture.Queries {
		started := time.Now()
		result := goldenQueryResult{ID: q.ID, Phase: phase, Query: q.Query, ExpectedEntry: q.ExpectedEntryID}
		found, err := t.Find(ctx, q.Query)
		if err == nil {
			containsExpected := false
			for _, e := range found.Raw {
				if e == nil || e.GetKind().IsAnchor() {
					continue
				}
				actual, ok := bySeq[e.GetID()]
				if !ok {
					err = errors.Join(err, fmt.Errorf("unknown source entry %s", e.GetID()))
					continue
				}
				result.ReturnedIDs = append(result.ReturnedIDs, actual.ID)
				if actual.Window != byID[q.ExpectedEntryID].Window {
					err = errors.Join(err, fmt.Errorf("returned unrelated scene %s", actual.Window))
				}
				containsExpected = containsExpected || actual.ID == q.ExpectedEntryID
			}
			if !containsExpected {
				err = errors.Join(err, fmt.Errorf("missing expected entry %s", q.ExpectedEntryID))
			}
		}
		result.DurationMS = time.Since(started).Milliseconds()
		result.Passed = err == nil
		if err != nil {
			result.Error = err.Error()
			failures = errors.Join(failures, fmt.Errorf("%s / %s: %w", phase, q.ID, err))
			fmt.Printf("  FAIL %s / %s: %v\n", phase, q.ID, err)
		} else {
			passed++
			fmt.Printf("  PASS %s / %s\n", phase, q.ID)
		}
		report.Results = append(report.Results, result)
	}
	fmt.Printf("  %s golden retrieval: %d/%d passed\n", phase, passed, len(fixture.Queries))
	return failures
}
