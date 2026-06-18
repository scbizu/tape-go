package entry

import "testing"

import "time"

func TestEntryKindIsAnchor(t *testing.T) {
	tests := map[EntryKind]bool{
		EntryAnchor:      true,
		"anchor:handoff": true,
		"anchorish":      false,
		EntryUser:        false,
	}
	for kind, want := range tests {
		if got := kind.IsAnchor(); got != want {
			t.Errorf("%q.IsAnchor() = %v, want %v", kind, got, want)
		}
	}
}

func TestNewEntryTimestamp(t *testing.T) {
	if NewEntry().GetTimestamp().IsZero() {
		t.Fatal("NewEntry() timestamp is zero")
	}

	want := time.Unix(123, 0)
	if got := NewEntry(WithEntryTimestamp(want)).GetTimestamp(); !got.Equal(want) {
		t.Fatalf("timestamp = %v, want %v", got, want)
	}
}
