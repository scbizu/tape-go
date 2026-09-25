package view

import (
	"context"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

// AnchorMaker derives an optional anchor from an assembled EntryView after the
// latest primary entry has been stored. Implementations may use classifiers,
// LLMs, or deterministic policies.
type AnchorMaker interface {
	MakeAnchor(context.Context, entry.EntryLike, EntryView) (entry.EntryLike, bool, error)
}
