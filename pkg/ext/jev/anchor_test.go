package jev

import "testing"

func TestMemoryStateIsZeroWithoutDecision(t *testing.T) {
	t.Parallel()

	if !(MemoryState{}).IsZero() {
		t.Fatal("empty MemoryState is not zero")
	}
	if !((MemoryState{Decisions: []string{"  "}}).IsZero()) {
		t.Fatal("MemoryState with a blank decision is not zero")
	}
	if (MemoryState{Decisions: []string{"retain durable fact"}}).IsZero() {
		t.Fatal("MemoryState with a decision is zero")
	}
}
