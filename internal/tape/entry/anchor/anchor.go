package anchor

import "time"

type Anchor struct {
	ID           string
	SessionID    string
	AtSeq        uint64
	PrevAnchorID string
	PhaseTag     string
	Summary      string
	State        map[string]any
	SourceSeqs   []uint64
	CreatedAt    time.Time
	Owner        string
}
