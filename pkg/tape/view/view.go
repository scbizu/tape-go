// Package view is the view assembler of tape
package view

import (
	"encoding/json"

	"github.com/scbizu/tape-go/pkg/tape/entry"
)

type EntryRange struct {
	SeqS uint64
	SeqE uint64
}

// EntryView describes a scoped entry view assembler
type EntryView struct {
	SessionId string
	Owner     string
	// Scope is the entry range index
	// Technically, [SeqS,SeqE)
	Scope EntryRange

	Raw []entry.EntryLike
	// Optional . If we need to integrate with some semantic search
	Summary string
}

func (ev EntryView) MarshalJSON() ([]byte, error) {
	return json.Marshal(ev.Raw)
}

// TapeView is a special view assemble without entry raw data.
// It describes the metadata info about the tape itself
type TapeView struct {
	SessionID string
	Owner     string
	Scope     EntryRange
}
