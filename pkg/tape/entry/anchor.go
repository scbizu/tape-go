package entry

import (
	"encoding/json"
	"fmt"
)

type AnchorKind uint32

const (
	AnchorKindHandoff AnchorKind = iota + 1
	AnchorKindCustom
)

func (ak AnchorKind) String() string {
	switch ak {
	case AnchorKindHandoff:
		return "anchor:handoff"
	case AnchorKindCustom:
		return "anchor:custom"
	}
	panic(fmt.Sprintf("unknown anchor kind: %d", ak))
}

type Anchor struct {
	Entry
	AnchorKind
	Ext json.RawMessage
}

func NewAnchor(
	seq uint64,
	owner string,
	kind AnchorKind,
	ext json.RawMessage,
) Entry {
	return Entry{
		Seq:   seq,
		Ek:    EntryKind(kind.String()),
		Text:  string(ext),
		Owner: owner,
	}
}

// HandoffAnchor describes the payload carried by an anchor:handoff entry.
//
// Tape exposes an entryView as the sliding window that an agent can currently
// work with. When that window grows too large, the agent or an upper layer can
// summarize the current entryView, write a handoff anchor for the covered range,
// archive the older entries, and then reset the sliding window after the anchor.
//
// Summary is the compact memory of the archived window. SeqRange records the
// original entries covered by that summary so a future agent can look back into
// the archive when the summary is not enough.
type HandoffAnchor struct {
	Summary    string
	SeqS, SeqE uint64
}
