package entry

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AnchorKind uint32

const (
	AnchorKindHandoff AnchorKind = iota + 1
	AnchorKindCustom
	AnchorKindJev
)

func (ak AnchorKind) String() string {
	switch ak {
	case AnchorKindHandoff:
		return "anchor:handoff"
	case AnchorKindCustom:
		return "anchor:custom"
	case AnchorKindJev:
		return "anchor:jev"
	}
	panic(fmt.Sprintf("unknown anchor kind: %d", ak))
}

type Anchor struct {
	Entry
	AnchorKind
	Ext json.RawMessage
}

func NewAnchor(
	seq Seq,
	owner string,
	kind AnchorKind,
	ext json.RawMessage,
) Entry {
	return Entry{
		Seq:       seq,
		Ek:        EntryKind(kind.String()),
		Text:      string(ext),
		Owner:     owner,
		Timestamp: time.Now(),
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
	SeqS, SeqE Seq
}

// JevMemoryState is the structured memory representation generated for Jev.
// A usable state always carries at least one retrieval decision.
type JevMemoryState struct {
	Overview       string   `json:"overview"`
	Facts          []string `json:"facts"`
	Decisions      []string `json:"decisions"`
	Constraints    []string `json:"constraints"`
	Preferences    []string `json:"preferences"`
	Results        []string `json:"results"`
	UnresolvedWork []string `json:"unresolved_work"`
}

func (s JevMemoryState) IsZero() bool {
	for _, decision := range s.Decisions {
		if strings.TrimSpace(decision) != "" {
			return false
		}
	}
	return true
}

// JevAnchor is a classifier-triggered memory checkpoint. Unlike a handoff
// anchor, it does not change the active view and is only consumed by Jev search.
type JevAnchor struct {
	State      JevMemoryState
	SeqS, SeqE Seq
}

// NewJevAnchor constructs an anchor:jev entry from its typed payload.
func NewJevAnchor(seq Seq, owner string, anchor JevAnchor) (Entry, error) {
	if anchor.State.IsZero() {
		return Entry{}, fmt.Errorf("entry: Jev anchor state is empty")
	}
	payload, err := json.Marshal(anchor)
	if err != nil {
		return Entry{}, fmt.Errorf("entry: encode Jev anchor: %w", err)
	}
	return NewAnchor(seq, owner, AnchorKindJev, payload), nil
}
