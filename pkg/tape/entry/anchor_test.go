package entry

import "testing"

func TestJevMemoryStateIsZeroWithoutDecision(t *testing.T) {
	t.Parallel()

	if !((JevMemoryState{Overview: "fact only", Facts: []string{"durable fact"}}).IsZero()) {
		t.Fatal("JevMemoryState without a decision is not zero")
	}
	if !((JevMemoryState{Decisions: []string{"  "}}).IsZero()) {
		t.Fatal("JevMemoryState with a blank decision is not zero")
	}
	if (JevMemoryState{Decisions: []string{"retain durable fact"}}).IsZero() {
		t.Fatal("JevMemoryState with a decision is zero")
	}
}
