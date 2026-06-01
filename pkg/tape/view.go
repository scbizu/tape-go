package tape

type ViewRequest struct {
	SessionID    string
	Task         string
	BudgetTokens int
}

type View struct {
	SessionID      string
	AnchorID       string
	IncludedSeqs   []uint64
	OmittedRanges  [][2]uint64
	DerivedSummary string
	Provenance     []uint64
}
