package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProtectedInputStoresWipeableBytesAndNeverRendersValue(t *testing.T) {
	input := newProtectedInput("passphrase", 64)
	input.Focus()
	input, _ = input.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sensitive-value")})
	if input.Value() != "sensitive-value" || strings.Contains(input.View(), "sensitive-value") {
		t.Fatalf("protected input value/view = %q/%q", input.Value(), input.View())
	}
	input.Clear()
	if input.Len() != 0 || strings.Contains(input.View(), "sensitive-value") {
		t.Fatal("protected input was not cleared")
	}
}
