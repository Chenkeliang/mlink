package tui

import (
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

type protectedInput struct {
	value       []byte
	placeholder string
	prompt      string
	limit       int
	focused     bool
}

func newProtectedInput(placeholder string, limit int) protectedInput {
	return protectedInput{placeholder: placeholder, prompt: "> ", limit: limit}
}

func (input *protectedInput) Focus() { input.focused = true }
func (input *protectedInput) Blur()  { input.focused = false }
func (input *protectedInput) SetValue(value string) {
	input.Clear()
	input.value = append([]byte(nil), []byte(value)...)
}
func (input protectedInput) Value() string { return string(input.value) }
func (input protectedInput) Bytes() []byte { return append([]byte(nil), input.value...) }
func (input protectedInput) Len() int      { return len(input.value) }
func (input *protectedInput) Clear() {
	wipe(input.value)
	input.value = nil
}
func (input protectedInput) View() string {
	if len(input.value) == 0 {
		return input.prompt + input.placeholder
	}
	return input.prompt + strings.Repeat("•", utf8.RuneCount(input.value))
}
func (input protectedInput) Update(key tea.KeyMsg) (protectedInput, tea.Cmd) {
	if !input.focused {
		return input, nil
	}
	switch key.Type {
	case tea.KeyRunes:
		value := []byte(string(key.Runes))
		if len(input.value)+len(value) <= input.limit {
			input.value = append(input.value, value...)
		}
		wipe(value)
	case tea.KeyBackspace, tea.KeyDelete:
		if len(input.value) != 0 {
			_, size := utf8.DecodeLastRune(input.value)
			if size <= 0 {
				size = 1
			}
			for index := len(input.value) - size; index < len(input.value); index++ {
				input.value[index] = 0
			}
			input.value = input.value[:len(input.value)-size]
		}
	case tea.KeyCtrlU:
		input.Clear()
	}
	return input, nil
}
