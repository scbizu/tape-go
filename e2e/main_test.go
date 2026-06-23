package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDeepSeekAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[provider.deepseek]\napi_key = \"from-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DEEPSEEK_API_KEY", "from-env")
	got, err := deepSeekAPIKey(path)
	if err != nil || got != "from-env" {
		t.Fatalf("environment key = %q, %v", got, err)
	}

	t.Setenv("DEEPSEEK_API_KEY", "")
	got, err = deepSeekAPIKey(path)
	if err != nil || got != "from-file" {
		t.Fatalf("file key = %q, %v", got, err)
	}
}

func TestChatUIBackspace(t *testing.T) {
	model := newChatUI(context.Background(), nil)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	model = updated.(chatUI)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	model = updated.(chatUI)

	if got := model.input.Value(); got != "h" {
		t.Fatalf("input after backspace = %q, want h", got)
	}
}
