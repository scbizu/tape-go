package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/scbizu/tape-go/pkg/llm"
	"github.com/scbizu/tape-go/pkg/provider/ds"
	jevprovider "github.com/scbizu/tape-go/pkg/provider/jev"
	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	bboltstore "github.com/scbizu/tape-go/pkg/tape/storage/bbolt"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/testsuite"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const ownerID = "bdd-user"
const sessionID = "bdd-session"

type storageBackend struct {
	name string
	new  func(context.Context, string) (storage.TapeStorage, error)
}

func newTape(ctx context.Context, backend storageBackend, dir string) (*tape.Tape, error) {
	store, err := backend.new(ctx, dir)
	if err != nil {
		return nil, err
	}
	t := &tape.Tape{TapeStorage: store, OwnerID: ownerID}
	if err := t.Init(ctx); err != nil {
		return nil, errors.Join(err, t.Close())
	}
	return t, nil
}

func TestTapeBehavior(t *testing.T) { runBehavior(t, "~@live") }
func TestLiveJEVBehavior(t *testing.T) {
	if os.Getenv("TAPE_E2E_LIVE") != "1" {
		t.Skip("set TAPE_E2E_LIVE=1 to run real JEV scenarios")
	}
	runBehavior(t, "@live")
}
func runBehavior(t *testing.T, tags string) {
	suite := godog.TestSuite{
		Name:                t.Name(),
		ScenarioInitializer: func(sc *godog.ScenarioContext) { initializeBehavior(sc, t) },
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"features"}, Tags: tags,
			Strict: true, TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("behavior scenarios failed")
	}
}

type behaviorState struct {
	ctx     context.Context
	cancel  context.CancelFunc
	dir     string
	tape    *tape.Tape
	backend storageBackend
	fixture testsuite.Fixture
	config  testsuite.Config
	seqs    map[string]entry.Seq
	anchors []testsuite.Anchor
	report  testsuite.Report
	phase   string
	started time.Time
}

func initializeBehavior(sc *godog.ScenarioContext, t *testing.T) {
	s := &behaviorState{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		*s = behaviorState{seqs: make(map[string]entry.Seq), phase: "before restart", started: time.Now()}
		s.ctx, s.cancel = context.WithTimeout(owner.WithOwnerId(ctx, ownerID), 5*time.Minute)
		var err error
		s.dir, err = os.MkdirTemp("", "tape-bdd-")
		return s.ctx, err
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, scenarioErr error) (context.Context, error) {
		defer s.cancel()
		var closeErr error
		if s.tape != nil {
			closeErr = s.tape.Close()
			s.tape = nil
		}
		s.report.DurationMS = time.Since(s.started).Milliseconds()
		if err := errors.Join(scenarioErr, closeErr); err != nil {
			s.report.Error = err.Error()
		}
		data, err := json.MarshalIndent(s.report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(s.dir, "report.json"), append(data, '\n'), 0600)
		}
		t.Logf("BDD tape and report: %s", s.dir)
		return ctx, errors.Join(closeErr, err)
	})
	sc.Step(`^a "(jsonl|bbolt)" tape$`, s.prepareBackend)
	sc.Step(`^the Lantern Road fixture with (\d+) source entries in (\d+) scenes$`, s.loadFixture)
	sc.Step(`^JEV checkpointing and retrieval are enabled$`, s.enableJEV)
	sc.Step(`^the conversation is recorded scene by scene$`, s.record)
	sc.Step(`^all source entries retain their IDs, kinds, content and order$`, s.checkSources)
	sc.Step(`^the tape is closed and reopened$`, s.restart)
	sc.Step(`^all recorded anchors retain their IDs, kinds and content$`, s.checkAnchors)
	sc.Step(`^at least (\d+) anchors each describe a distinct complete scene$`, s.checkScenes)
	sc.Step(`^all (\d+) golden queries return their expected source without unrelated scenes$`, s.checkQueries)
	initializeAgentSteps(sc, s)
}
func (s *behaviorState) prepareBackend(name string) error {
	s.backend = storageBackend{name: name}
	switch name {
	case "jsonl":
		s.backend.new = func(_ context.Context, dir string) (storage.TapeStorage, error) {
			return jsonl.NewJSONLStorage(sessionID, dir)
		}
	case "bbolt":
		s.backend.new = func(_ context.Context, dir string) (storage.TapeStorage, error) {
			return bboltstore.NewBboltStorage(sessionID, filepath.Join(dir, "tape.db"))
		}
	}
	s.config = testsuite.Config{Name: "storage/" + name, Open: func(ctx context.Context, dir string) (*tape.Tape, error) { return newTape(ctx, s.backend, dir) }}
	s.report.Scenario = s.config.Name
	return nil
}
func (s *behaviorState) loadFixture(count, scenes int) error {
	var err error
	s.fixture, err = testsuite.ParseFixture(tapeGoldenJSON)
	if err != nil {
		return err
	}
	if s.fixture.SourceEntries != count || len(s.fixture.Windows()) != scenes {
		return fmt.Errorf("fixture does not contain %d entries in %d scenes", count, scenes)
	}
	s.report.Title = s.fixture.Title
	return nil
}
func (s *behaviorState) enableJEV() error {
	key, err := jevAPIKey("config.toml")
	if err != nil {
		return err
	}
	client, err := jevprovider.NewClient(key)
	if err != nil {
		return err
	}
	var summarizer llm.Summarizer = projectionSummarizer{}
	key, err = optionalDeepSeekAPIKey("config.toml")
	if err != nil {
		return err
	}
	if key != "" {
		summarizer, err = ds.NewModel(key, os.Getenv("DEEPSEEK_MODEL"))
		if err != nil {
			return err
		}
	}
	s.config = jevGoldenConfig(s.backend, s.fixture, summarizer, client)
	s.report.Scenario = s.config.Name
	s.report.SearchEnabled = true
	s.report.MinAnchors = s.config.MinAnchors
	return nil
}
func (s *behaviorState) record() error {
	var err error
	s.tape, err = s.config.Open(s.ctx, s.dir)
	if err != nil {
		return err
	}
	for _, window := range s.fixture.Windows() {
		meta, err := s.tape.Get(s.ctx)
		if err != nil {
			return err
		}
		s.tape.SetView(view.EntryRange{SeqS: meta.Scope.SeqE.Next()})
		for _, e := range window.Entries {
			stored, err := s.tape.Store(s.ctx, entry.NewEntry(entry.WithEntryKind(e.Kind), entry.WithEntryContent(e.Text)))
			if err != nil {
				return fmt.Errorf("store %s: %w", e.ID, err)
			}
			if stored == nil || stored.GetID().IsZero() {
				return fmt.Errorf("source %s has no persisted ID", e.ID)
			}
			s.seqs[e.ID] = stored.GetID()
			s.report.SourceEntries++
		}
	}
	return nil
}
func (s *behaviorState) all() (view.EntryView, error) {
	meta, err := s.tape.Get(s.ctx)
	if err != nil {
		return view.EntryView{}, err
	}
	return s.tape.Range(s.ctx, view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: meta.Scope.SeqE.Next()})
}
func (s *behaviorState) checkSources() error {
	all, err := s.all()
	if err != nil {
		return err
	}
	var anchors []testsuite.Anchor
	index := 0
	for _, e := range all.Raw {
		if e == nil {
			return errors.New("nil persisted entry")
		}
		if e.GetKind().IsAnchor() {
			anchors = append(anchors, testsuite.Anchor{Seq: e.GetID(), Kind: e.GetKind(), Content: e.GetSummary()})
			continue
		}
		if index >= len(s.fixture.Entries) {
			return errors.New("extra source entry")
		}
		want := s.fixture.Entries[index]
		if e.GetID() != s.seqs[want.ID] || e.GetKind() != want.Kind || e.GetSummary() != want.Text {
			return fmt.Errorf("source %s changed during %s", want.ID, s.phase)
		}
		index++
	}
	if index != s.fixture.SourceEntries {
		return fmt.Errorf("got %d source entries, want %d", index, s.fixture.SourceEntries)
	}
	if s.phase == "before restart" {
		s.anchors = anchors
		s.report.Anchors = anchors
	}
	return nil
}
func (s *behaviorState) restart() error {
	if err := s.tape.Close(); err != nil {
		return err
	}
	s.tape = nil
	var err error
	s.tape, err = s.config.Open(s.ctx, s.dir)
	s.phase = "after restart"
	return err
}
func (s *behaviorState) checkAnchors() error {
	all, err := s.all()
	if err != nil {
		return err
	}
	var restored []testsuite.Anchor
	for _, e := range all.Raw {
		if e.GetKind().IsAnchor() {
			restored = append(restored, testsuite.Anchor{Seq: e.GetID(), Kind: e.GetKind(), Content: e.GetSummary()})
		}
	}
	if !reflect.DeepEqual(s.anchors, restored) {
		return errors.New("anchor IDs, kinds or content changed after restart")
	}
	return nil
}
func (s *behaviorState) checkScenes(minimum int) error {
	if len(s.anchors) < minimum {
		return fmt.Errorf("got %d anchors, want at least %d", len(s.anchors), minimum)
	}
	return validateJevGoldenAnchors(s.fixture, s.seqs, s.anchors)
}
func (s *behaviorState) checkQueries(count int) error {
	if len(s.fixture.Queries) != count {
		return fmt.Errorf("fixture has %d queries, want %d", len(s.fixture.Queries), count)
	}
	bySeq := make(map[entry.Seq]testsuite.Entry)
	byID := make(map[string]testsuite.Entry)
	for _, e := range s.fixture.Entries {
		bySeq[s.seqs[e.ID]] = e
		byID[e.ID] = e
	}
	var failures error
	for _, q := range s.fixture.Queries {
		started := time.Now()
		r := testsuite.QueryResult{ID: q.ID, Phase: s.phase, Query: q.Query, ExpectedEntry: q.ExpectedEntryID}
		found, err := s.config.Search(s.ctx, s.tape, q.Query)
		expected := false
		if err == nil {
			for _, e := range found.Raw {
				if e == nil {
					err = errors.Join(err, errors.New("nil search result"))
					continue
				}
				if e.GetKind().IsAnchor() {
					continue
				}
				actual, ok := bySeq[e.GetID()]
				if !ok {
					err = errors.Join(err, fmt.Errorf("unknown entry %s", e.GetID()))
					continue
				}
				r.ReturnedIDs = append(r.ReturnedIDs, actual.ID)
				if actual.Window != byID[q.ExpectedEntryID].Window {
					err = errors.Join(err, fmt.Errorf("unrelated scene %s", actual.Window))
				}
				expected = expected || actual.ID == q.ExpectedEntryID
			}
			if !expected {
				err = errors.Join(err, fmt.Errorf("missing source %s", q.ExpectedEntryID))
			}
		}
		r.DurationMS = time.Since(started).Milliseconds()
		r.Passed = err == nil
		if err != nil {
			r.Error = err.Error()
			failures = errors.Join(failures, fmt.Errorf("%s / %s: %w", s.phase, q.ID, err))
		}
		s.report.Results = append(s.report.Results, r)
	}
	return failures
}
