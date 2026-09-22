package finder

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fixedAnchorDecider float64

func (d fixedAnchorDecider) ShouldAnchor(context.Context, JevViewProjection) (float64, error) {
	return float64(d), nil
}

func (d fixedAnchorDecider) ValidateSummary(context.Context, JevViewProjection, entry.JevMemoryState) (float64, error) {
	return float64(d), nil
}

type fixedSummarizer entry.JevMemoryState

func (s fixedSummarizer) Summarize(context.Context, JevViewProjection) (entry.JevMemoryState, error) {
	return entry.JevMemoryState(s), nil
}

type recordingAnchorDecider struct {
	projection JevViewProjection
}

func (d *recordingAnchorDecider) ShouldAnchor(_ context.Context, projection JevViewProjection) (float64, error) {
	d.projection = projection
	return .9, nil
}

func (*recordingAnchorDecider) ValidateSummary(context.Context, JevViewProjection, entry.JevMemoryState) (float64, error) {
	return .9, nil
}

func TestJevAnchorPolicyDecidesFromWholeView(t *testing.T) {
	t.Parallel()

	first := entry.NewEntry(
		entry.WithEntryID(entry.SeqFromUint64(4)),
		entry.WithEntryContent("earlier durable fact"),
	)
	latest := entry.NewEntry(
		entry.WithEntryID(entry.SeqFromUint64(5)),
		entry.WithEntryContent("acknowledged"),
	)
	decider := &recordingAnchorDecider{}
	_, ok, err := NewJevAnchorPolicy(
		decider,
		fixedSummarizer{Decisions: []string{"retain earlier durable fact"}},
		.7,
	).MakeAnchor(context.Background(), latest, view.EntryView{
		Scope: view.EntryRange{SeqS: entry.SeqFromUint64(4), SeqE: entry.SeqFromUint64(6)},
		Raw:   []entry.EntryLike{first, latest},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(decider.projection.Entries) != 2 ||
		decider.projection.Entries[0].Summary != "earlier durable fact" ||
		decider.projection.Entries[1].Summary != "acknowledged" {
		t.Fatalf("decision projection = %#v, ok = %v", decider.projection, ok)
	}
}

func TestJevAnchorPolicyCreatesJevAnchor(t *testing.T) {
	t.Parallel()

	memoryState := fixedSummarizer{
		Overview:  "Database region: Tokyo.",
		Facts:     []string{"Production database region is Tokyo."},
		Decisions: []string{"Retain the production database region."},
	}
	policy := NewJevAnchorPolicy(fixedAnchorDecider(.9), memoryState, .7)
	latest := entry.NewEntry(
		entry.WithEntryID(entry.SeqFromUint64(4)),
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent("Remember the production database is in Tokyo."),
		entry.WithEntryOwner("owner-a"),
	)
	anchor, ok, err := policy.MakeAnchor(context.Background(), entry.NewEntry(
		entry.WithEntryContent(latest.GetSummary()),
		entry.WithEntryOwner(latest.GetOwner()),
	), view.EntryView{
		Owner: "owner-a",
		Scope: view.EntryRange{SeqS: entry.SeqFromUint64(4), SeqE: entry.SeqFromUint64(5)},
		Raw:   []entry.EntryLike{latest},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !ok || anchor.GetKind() != entry.EntryKind(entry.AnchorKindJev.String()) {
		t.Fatalf("anchor = %#v, ok = %v", anchor, ok)
	}
	var payload entry.JevAnchor
	if err := json.Unmarshal([]byte(anchor.GetSummary()), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.State.Overview != "Database region: Tokyo." ||
		payload.SeqS != entry.SeqFromUint64(4) ||
		payload.SeqE != entry.SeqFromUint64(5) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestJevAnchorPolicyHonorsThreshold(t *testing.T) {
	t.Parallel()

	_, ok, err := NewJevAnchorPolicy(fixedAnchorDecider(.4), fixedSummarizer{Decisions: []string{"unused"}}, .7).MakeAnchor(
		context.Background(),
		entry.NewEntry(entry.WithEntryContent("transient")),
		view.EntryView{
			Scope: view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)},
			Raw:   []entry.EntryLike{entry.NewEntry(entry.WithEntryContent("transient"))},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("entry below threshold was anchored")
	}
}

type splitAnchorDecider struct {
	should, faithful float64
}

func (d splitAnchorDecider) ShouldAnchor(context.Context, JevViewProjection) (float64, error) {
	return d.should, nil
}

func (d splitAnchorDecider) ValidateSummary(context.Context, JevViewProjection, entry.JevMemoryState) (float64, error) {
	return d.faithful, nil
}

func TestJevAnchorPolicyRejectsUnfaithfulSummary(t *testing.T) {
	t.Parallel()

	e := entry.NewEntry(entry.WithEntryContent("source fact"))
	_, ok, err := NewJevAnchorPolicy(
		splitAnchorDecider{should: .95, faithful: .2},
		fixedSummarizer{Decisions: []string{"unsupported claim"}},
		.7,
	).MakeAnchor(context.Background(), e, view.EntryView{
		Scope: view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)},
		Raw:   []entry.EntryLike{e},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unfaithful summary became a Jev anchor")
	}
}

func TestJevAnchorPolicyValidatesConfiguration(t *testing.T) {
	t.Parallel()

	e := entry.NewEntry(entry.WithEntryContent("source fact"))
	memory := view.EntryView{Raw: []entry.EntryLike{e}}
	tests := []struct {
		name   string
		policy JevAnchorPolicy
	}{
		{name: "missing decider", policy: NewJevAnchorPolicy(nil, fixedSummarizer{Decisions: []string{"summary"}}, .7)},
		{name: "missing summarizer", policy: NewJevAnchorPolicy(fixedAnchorDecider(.9), nil, .7)},
		{name: "invalid threshold", policy: NewJevAnchorPolicy(fixedAnchorDecider(.9), fixedSummarizer{Decisions: []string{"summary"}}, 2)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := test.policy.MakeAnchor(context.Background(), e, memory); err == nil {
				t.Fatal("MakeAnchor accepted invalid policy")
			}
		})
	}
}
