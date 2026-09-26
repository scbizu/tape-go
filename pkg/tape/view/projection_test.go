package view

import (
	"testing"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

func TestEntryViewProjectSourceEntries(t *testing.T) {
	scope := EntryRange{SeqS: entry.SeqFromUint64(2), SeqE: entry.SeqFromUint64(5)}
	source := entry.NewEntry(
		entry.WithEntryID(entry.SeqFromUint64(2)),
		entry.WithEntryKind(entry.EntryUser),
		entry.WithEntryContent("Keep the Tokyo region."),
	)
	anchor := entry.NewAnchor(entry.SeqFromUint64(3), "owner", entry.AnchorKindCustom, nil)
	projection, err := (EntryView{
		Scope: scope,
		Raw:   []entry.EntryLike{nil, source, anchor},
	}).Project()
	if err != nil {
		t.Fatal(err)
	}
	if projection.Scope != scope || len(projection.Entries) != 1 || projection.Entries[0] != (ProjectedEntry{
		Seq: source.GetID(), Kind: source.GetKind(), Summary: source.GetSummary(),
	}) {
		t.Fatalf("projection = %#v", projection)
	}
	if _, err := (EntryView{Raw: []entry.EntryLike{anchor}}).Project(); err == nil {
		t.Fatal("Project accepted a view without source entries")
	}
}
