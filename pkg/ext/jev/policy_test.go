package jev

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fixedAnchorDecider float64

func (d fixedAnchorDecider) ShouldAnchor(context.Context, ViewProjection) (float64, error) {
	return float64(d), nil
}

func (d fixedAnchorDecider) ValidateSummary(context.Context, ViewProjection, MemoryState) (float64, error) {
	return float64(d), nil
}

type fixedSummarizer MemoryState

func (s fixedSummarizer) Summarize(context.Context, ViewProjection) (MemoryState, error) {
	return MemoryState(s), nil
}

type recordingAnchorDecider struct {
	projection ViewProjection
}

func (d *recordingAnchorDecider) ShouldAnchor(_ context.Context, projection ViewProjection) (float64, error) {
	d.projection = projection
	return .9, nil
}

func (*recordingAnchorDecider) ValidateSummary(context.Context, ViewProjection, MemoryState) (float64, error) {
	return .9, nil
}

func TestAnchorPolicyDecidesFromWholeView(t *testing.T) {
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
	_, ok, err := NewAnchorPolicy(
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

func TestAnchorPolicyCreatesJevAnchor(t *testing.T) {
	t.Parallel()

	memoryState := fixedSummarizer{
		Decisions: []string{"The production database region is Tokyo."},
	}
	policy := NewAnchorPolicy(fixedAnchorDecider(.9), memoryState, .7)
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
	if !ok || anchor.GetKind() != entry.EntryKind(string(Kind)) {
		t.Fatalf("anchor = %#v, ok = %v", anchor, ok)
	}
	var payload Anchor
	if err := json.Unmarshal([]byte(anchor.GetSummary()), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.State.Decisions) != 1 || payload.State.Decisions[0] != "The production database region is Tokyo." ||
		payload.SeqS != entry.SeqFromUint64(4) ||
		payload.SeqE != entry.SeqFromUint64(5) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestAnchorPolicyHonorsThreshold(t *testing.T) {
	t.Parallel()

	_, ok, err := NewAnchorPolicy(fixedAnchorDecider(.4), fixedSummarizer{Decisions: []string{"unused"}}, .7).MakeAnchor(
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

func (d splitAnchorDecider) ShouldAnchor(context.Context, ViewProjection) (float64, error) {
	return d.should, nil
}

func (d splitAnchorDecider) ValidateSummary(context.Context, ViewProjection, MemoryState) (float64, error) {
	return d.faithful, nil
}

func TestAnchorPolicyRejectsUnfaithfulSummary(t *testing.T) {
	t.Parallel()

	e := entry.NewEntry(entry.WithEntryContent("source fact"))
	_, ok, err := NewAnchorPolicy(
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

func TestAnchorPolicyValidatesConfiguration(t *testing.T) {
	t.Parallel()

	e := entry.NewEntry(entry.WithEntryContent("source fact"))
	memory := view.EntryView{Raw: []entry.EntryLike{e}}
	tests := []struct {
		name   string
		policy AnchorPolicy
	}{
		{name: "missing decider", policy: NewAnchorPolicy(nil, fixedSummarizer{Decisions: []string{"summary"}}, .7)},
		{name: "missing summarizer", policy: NewAnchorPolicy(fixedAnchorDecider(.9), nil, .7)},
		{name: "invalid threshold", policy: NewAnchorPolicy(fixedAnchorDecider(.9), fixedSummarizer{Decisions: []string{"summary"}}, 2)},
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
