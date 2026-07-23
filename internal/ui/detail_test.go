package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDetailViewRendersOneBreadcrumbAndRows(t *testing.T) {
	m := detailModel{Detail: Detail{Title: "rec-deploy / MCP / Status", Rows: [][2]string{{"remote", "off"}}}, width: 80}
	view := m.View()
	if strings.Count(view, "rec-deploy / MCP / Status") != 1 || !strings.Contains(view, "remote") || !strings.Contains(view, "off") {
		t.Errorf("unexpected detail view:\n%s", view)
	}
}

func TestDetailNavigation(t *testing.T) {
	model, cmd := (detailModel{}).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !model.(detailModel).closing || model.(detailModel).quit {
		t.Error("enter did not navigate back cleanly")
	}
	model, cmd = (detailModel{}).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil || !model.(detailModel).closing || model.(detailModel).quit {
		t.Error("escape did not navigate back cleanly")
	}
	model, cmd = (detailModel{}).Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || !model.(detailModel).quit {
		t.Error("ctrl+c did not quit")
	}
}
