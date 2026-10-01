package testsuite

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

// Config separates the fixture and assertions from storage and search providers.
// Open must return an initialized tape and reopen the same persisted tape for
// the supplied directory. Run closes each returned tape.
// Search is optional: nil runs persistence checks without claiming query coverage.
type Config struct {
	Name            string
	Open            func(context.Context, string) (*tape.Tape, error)
	Search          func(context.Context, *tape.Tape, string) (view.EntryView, error)
	MinAnchors      int
	ValidateAnchors func(Fixture, map[string]entry.Seq, []Anchor) error
	Progress        func(string)
}

type Anchor struct {
	Seq     entry.Seq       `json:"seq"`
	Kind    entry.EntryKind `json:"kind"`
	Content string          `json:"content"`
}

type QueryResult struct {
	ID            string   `json:"id"`
	Phase         string   `json:"phase"`
	Query         string   `json:"query"`
	ExpectedEntry string   `json:"expected_entry"`
	ReturnedIDs   []string `json:"returned_ids"`
	Passed        bool     `json:"passed"`
	DurationMS    int64    `json:"duration_ms"`
	Error         string   `json:"error,omitempty"`
}

type Report struct {
	Title         string        `json:"title"`
	Scenario      string        `json:"scenario"`
	SourceEntries int           `json:"source_entries"`
	MinAnchors    int           `json:"min_anchors"`
	SearchEnabled bool          `json:"search_enabled"`
	Anchors       []Anchor      `json:"anchors"`
	Results       []QueryResult `json:"results"`
	DurationMS    int64         `json:"duration_ms"`
	Error         string        `json:"error,omitempty"`
}

func Run(ctx context.Context, fixture Fixture, config Config, dir string) (report Report, runErr error) {
	started := time.Now()
	report = Report{Title: fixture.Title, Scenario: config.Name, MinAnchors: config.MinAnchors, SearchEnabled: config.Search != nil}
	defer func() {
		report.DurationMS = time.Since(started).Milliseconds()
		if runErr != nil {
			report.Error = runErr.Error()
		}
	}()
	if err := fixture.Validate(); err != nil {
		return report, err
	}
	if config.Open == nil || config.MinAnchors < 0 {
		return report, errors.New("golden suite requires a tape factory and nonnegative anchor minimum")
	}
	progress := func(format string, args ...any) {
		if config.Progress != nil {
			config.Progress(fmt.Sprintf(format, args...))
		}
	}
	t, err := config.Open(ctx, dir)
	if err != nil {
		return report, err
	}
	defer func() {
		if t != nil {
			runErr = errors.Join(runErr, t.Close())
		}
	}()
	seqs := make(map[string]entry.Seq)
	for _, window := range fixture.Windows() {
		meta, err := t.Get(ctx)
		if err != nil {
			return report, err
		}
		t.SetView(view.EntryRange{SeqS: meta.Scope.SeqE.Next()})
		for _, e := range window.Entries {
			stored, err := t.Store(ctx, entry.NewEntry(entry.WithEntryKind(e.Kind), entry.WithEntryContent(e.Text)))
			if err != nil {
				return report, fmt.Errorf("store %s: %w", e.ID, err)
			}
			if stored == nil || stored.GetID().IsZero() {
				return report, fmt.Errorf("stored %s has no ID", e.ID)
			}
			seqs[e.ID] = stored.GetID()
			report.SourceEntries++
		}
		progress("  stored %d/%d source entries (%s)", report.SourceEntries, fixture.SourceEntries, window.ID)
	}
	report.Anchors, err = validateTape(ctx, t, fixture, config, seqs)
	if err != nil {
		return report, err
	}
	progress("  verified %d source entries and %d anchors", report.SourceEntries, len(report.Anchors))
	beforeErr := checkRetrieval(ctx, t, fixture, config, seqs, "before restart", &report, progress)
	if err := t.Close(); err != nil {
		return report, errors.Join(beforeErr, err)
	}
	t = nil
	t, err = config.Open(ctx, dir)
	if err != nil {
		return report, errors.Join(beforeErr, fmt.Errorf("reopen: %w", err))
	}
	restored, err := validateTape(ctx, t, fixture, config, seqs)
	if err != nil {
		return report, errors.Join(beforeErr, fmt.Errorf("validate restored tape: %w", err))
	}
	if !reflect.DeepEqual(report.Anchors, restored) {
		return report, errors.Join(beforeErr, errors.New("anchor IDs, kinds, or contents changed after restart"))
	}
	return report, errors.Join(beforeErr, checkRetrieval(ctx, t, fixture, config, seqs, "after restart", &report, progress))
}

func validateTape(ctx context.Context, t *tape.Tape, fixture Fixture, config Config, seqs map[string]entry.Seq) ([]Anchor, error) {
	meta, err := t.Get(ctx)
	if err != nil {
		return nil, err
	}
	all, err := t.Range(ctx, view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: meta.Scope.SeqE.Next()})
	if err != nil {
		return nil, err
	}
	var anchors []Anchor
	sourceIndex := 0
	for _, e := range all.Raw {
		if e == nil {
			return anchors, errors.New("tape contains a nil entry")
		}
		if e.GetKind().IsAnchor() {
			anchors = append(anchors, Anchor{Seq: e.GetID(), Kind: e.GetKind(), Content: e.GetSummary()})
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
	if sourceIndex != fixture.SourceEntries || len(anchors) < config.MinAnchors {
		return anchors, fmt.Errorf("golden tape has %d/%d source entries and %d/%d required anchors", sourceIndex, fixture.SourceEntries, len(anchors), config.MinAnchors)
	}
	if config.ValidateAnchors != nil {
		return anchors, config.ValidateAnchors(fixture, seqs, anchors)
	}
	return anchors, nil
}

func checkRetrieval(ctx context.Context, t *tape.Tape, fixture Fixture, config Config, seqs map[string]entry.Seq, phase string, report *Report, progress func(string, ...any)) error {
	if config.Search == nil {
		progress("  %s: persistence verified (search not configured)", phase)
		return nil
	}
	byID := make(map[string]Entry)
	bySeq := make(map[entry.Seq]Entry)
	for _, e := range fixture.Entries {
		byID[e.ID] = e
		bySeq[seqs[e.ID]] = e
	}
	var failures error
	passed := 0
	for _, q := range fixture.Queries {
		started := time.Now()
		result := QueryResult{ID: q.ID, Phase: phase, Query: q.Query, ExpectedEntry: q.ExpectedEntryID}
		found, err := config.Search(ctx, t, q.Query)
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
					err = errors.Join(err, fmt.Errorf("returned unrelated window %s", actual.Window))
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
			progress("  FAIL %s / %s: %v", phase, q.ID, err)
		} else {
			passed++
			progress("  PASS %s / %s", phase, q.ID)
		}
		report.Results = append(report.Results, result)
	}
	progress("  %s golden retrieval: %d/%d passed", phase, passed, len(fixture.Queries))
	return failures
}
