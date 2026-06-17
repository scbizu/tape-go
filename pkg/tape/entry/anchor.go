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
