package owner

import "testing"

func TestUserID(t *testing.T) {
	if got := UserID("alice"); got != "u:alice" {
		t.Fatalf("UserID() = %q, want %q", got, "u:alice")
	}
	if got := UserID("u:alice"); got != "u:alice" {
		t.Fatalf("UserID() duplicated prefix: %q", got)
	}
	if !IsUserID("u:alice") || IsUserID(SystemAgent) {
		t.Fatal("IsUserID() failed to distinguish user and system owners")
	}
}
