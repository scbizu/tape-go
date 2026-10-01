package testsuite

import (
	"context"
	"strings"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

func smallFixture() Fixture {
	return Fixture{Title: "Two conversations", SourceEntries: 3, Entries: []Entry{
		{ID: "first", Window: "one", Kind: entry.EntryUser, Text: "Hello"},
		{ID: "second", Window: "one", Kind: entry.EntryAssistant, Text: "The river crossing cost seven coins."},
		{ID: "third", Window: "two", Kind: entry.EntryUser, Text: "The lake crossing cost eleven tokens."},
	}, Queries: []Query{{ID: "river", Query: "River fare?", ExpectedEntryID: "second"}}}
}

func testConfig() Config {
	return Config{Name: "jsonl", Open: func(ctx context.Context, dir string) (*tape.Tape, error) {
		base, err := jsonl.NewJSONLStorage("session", dir)
		if err != nil {
			return nil, err
		}
		t := &tape.Tape{OwnerID: "owner", TapeStorage: base}
		if err := t.Init(ctx); err != nil {
			t.Close()
			return nil, err
		}
		return t, nil
	}}
}

func TestStorageScenarioDoesNotClaimSearchCoverage(t *testing.T) {
	report, err := Run(owner.WithOwnerId(context.Background(), "owner"), smallFixture(), testConfig(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if report.SourceEntries != 3 || report.SearchEnabled || len(report.Results) != 0 {
		t.Fatalf("storage report: %+v", report)
	}
}

func TestGoldenRejectsCrossWindowRetrievalInBothPhases(t *testing.T) {
	config := testConfig()
	config.Search = func(ctx context.Context, t *tape.Tape, _ string) (view.EntryView, error) {
		return t.Range(ctx, view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(4)})
	}
	report, err := Run(owner.WithOwnerId(context.Background(), "owner"), smallFixture(), config, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "unrelated window") || len(report.Results) != 2 {
		t.Fatalf("report %+v, error %v", report, err)
	}
	for _, result := range report.Results {
		if result.Passed {
			t.Fatal("cross-window retrieval passed")
		}
	}
}

func TestGoldenRejectsChangedTapeAfterRestart(t *testing.T) {
	config := testConfig()
	open := config.Open
	calls := 0
	config.Open = func(ctx context.Context, dir string) (*tape.Tape, error) {
		t, err := open(ctx, dir)
		if err != nil {
			return nil, err
		}
		calls++
		if calls == 2 {
			if _, err := t.Store(ctx, entry.NewEntry(entry.WithEntryKind(entry.EntryUser), entry.WithEntryContent("unexpected"))); err != nil {
				t.Close()
				return nil, err
			}
		}
		return t, nil
	}
	if _, err := Run(owner.WithOwnerId(context.Background(), "owner"), smallFixture(), config, t.TempDir()); err == nil || !strings.Contains(err.Error(), "extra source entry") {
		t.Fatalf("restart error: %v", err)
	}
}

func TestFixtureRejectsInvalidQueryBeforeOpeningTape(t *testing.T) {
	fixture := smallFixture()
	fixture.Queries[0].ExpectedEntryID = "missing"
	config := Config{Open: func(context.Context, string) (*tape.Tape, error) {
		t.Fatal("invalid fixture opened a tape")
		return nil, nil
	}}
	if _, err := Run(context.Background(), fixture, config, t.TempDir()); err == nil {
		t.Fatal("unknown golden source accepted")
	}
}
