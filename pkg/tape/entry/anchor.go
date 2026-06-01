package entry

import (
	"encoding/json"
	"fmt"
)

type AnchorKind uint32

const (
	AnchorKindHandoff AnchorKind = iota + 1
)

func (ak AnchorKind) String() string {
	switch ak {
	case AnchorKindHandoff:
		return "anchor:handoff"
	}
	panic(fmt.Sprintf("unknown anchor kind: %d", ak))
}

type Anchor struct {
	Entry
	AnchorKind
	Ext json.RawMessage
}
