// Package testsuite runs reusable, provider-independent golden tape scenarios.
package testsuite

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

type Entry struct {
	ID     string          `json:"id"`
	Window string          `json:"window"`
	Kind   entry.EntryKind `json:"kind"`
	Text   string          `json:"text"`
}

type Query struct {
	ID              string `json:"id"`
	Query           string `json:"query"`
	ExpectedEntryID string `json:"expected_entry_id"`
}

type Fixture struct {
	Title         string  `json:"title"`
	SourceEntries int     `json:"source_entries"`
	Entries       []Entry `json:"entries"`
	Queries       []Query `json:"queries"`
}

type Window struct {
	ID      string
	Entries []Entry
}

func ParseFixture(data []byte) (Fixture, error) {
	var fixture Fixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return fixture, err
	}
	return fixture, fixture.Validate()
}

func (f Fixture) Validate() error {
	if f.Title == "" || f.SourceEntries <= 0 || len(f.Entries) != f.SourceEntries {
		return errors.New("fixture title and source entry count must match its entries")
	}
	entries := make(map[string]bool)
	windows := make(map[string]bool)
	for i, e := range f.Entries {
		if e.ID == "" || e.Window == "" || e.Text == "" || e.Kind == "" || e.Kind.IsAnchor() || entries[e.ID] {
			return fmt.Errorf("invalid or duplicate fixture entry %q", e.ID)
		}
		entries[e.ID] = true
		if i == 0 || e.Window != f.Entries[i-1].Window {
			if windows[e.Window] {
				return fmt.Errorf("noncontiguous fixture window %q", e.Window)
			}
			windows[e.Window] = true
		}
	}
	queries := make(map[string]bool)
	for _, q := range f.Queries {
		if !entries[q.ExpectedEntryID] || q.ID == "" || q.Query == "" || queries[q.ID] {
			return fmt.Errorf("invalid or duplicate fixture query %q", q.ID)
		}
		queries[q.ID] = true
	}
	return nil
}

func (f Fixture) Windows() []Window {
	var windows []Window
	for _, e := range f.Entries {
		if len(windows) == 0 || windows[len(windows)-1].ID != e.Window {
			windows = append(windows, Window{ID: e.Window})
		}
		last := &windows[len(windows)-1]
		last.Entries = append(last.Entries, e)
	}
	return windows
}
