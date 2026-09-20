package finder

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type fixedAnchorDecider float64

func (d fixedAnchorDecider) ShouldAnchor(context.Context, string) (float64, error) {
	return float64(d), nil
}

func (d fixedAnchorDecider) ValidateSummary(context.Context, json.RawMessage, json.RawMessage) (float64, error) {
	return float64(d), nil
}

type fixedSummarizer string

func (s fixedSummarizer) Summarize(context.Context, string) (string, error) {
	return string(s), nil
}

func TestJevAnchorPolicyCreatesJevAnchor(t *testing.T) {
	t.Parallel()

	memoryState := `{"overview":"Database region: Tokyo.","facts":["Production database region is Tokyo."],"decisions":[],"constraints":[],"preferences":[],"results":[],"unresolved_work":[]}`
	policy := NewJevAnchorPolicy(fixedAnchorDecider(.9), fixedSummarizer(memoryState), .7)
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
	if string(payload.State) != memoryState ||
		payload.SeqS != entry.SeqFromUint64(4) ||
		payload.SeqE != entry.SeqFromUint64(5) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestJevAnchorPolicyHonorsThreshold(t *testing.T) {
	t.Parallel()

	_, ok, err := NewJevAnchorPolicy(fixedAnchorDecider(.4), fixedSummarizer("unused"), .7).MakeAnchor(
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

func (d splitAnchorDecider) ShouldAnchor(context.Context, string) (float64, error) {
	return d.should, nil
}

func (d splitAnchorDecider) ValidateSummary(context.Context, json.RawMessage, json.RawMessage) (float64, error) {
	return d.faithful, nil
}

func TestJevAnchorPolicyRejectsUnfaithfulSummary(t *testing.T) {
	t.Parallel()

	e := entry.NewEntry(entry.WithEntryContent("source fact"))
	_, ok, err := NewJevAnchorPolicy(
		splitAnchorDecider{should: .95, faithful: .2},
		fixedSummarizer(`{"overview":"unsupported claim"}`),
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

func TestJevAnchorPolicyRejectsUnstructuredSummary(t *testing.T) {
	t.Parallel()

	e := entry.NewEntry(entry.WithEntryContent("source fact"))
	_, ok, err := NewJevAnchorPolicy(
		fixedAnchorDecider(.95),
		fixedSummarizer("plain-text summary"),
		.7,
	).MakeAnchor(context.Background(), e, view.EntryView{
		Scope: view.EntryRange{SeqS: entry.SeqFromUint64(1), SeqE: entry.SeqFromUint64(2)},
		Raw:   []entry.EntryLike{e},
	})
	if err == nil || !strings.Contains(err.Error(), "valid JSON") {
		t.Fatalf("error = %v, want structured Jev state error", err)
	}
	if ok {
		t.Fatal("unstructured summary became a Jev anchor")
	}
}
