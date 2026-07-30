package ui

import (
	"strings"
	"testing"
)

func TestDetailViewRendersOneBreadcrumbAndRows(t *testing.T) {
	m := detailModel{Detail: Detail{Title: "rec-deploy / MCP / Status", Rows: [][2]string{{"remote", "off"}}}, width: 80}
	view := m.View().Content
	if strings.Count(view, "rec-deploy / MCP / Status") != 1 || !strings.Contains(view, "remote") || !strings.Contains(view, "off") {
		t.Errorf("unexpected detail view:\n%s", view)
	}
}

func TestDetailNavigation(t *testing.T) {
	model, cmd := (detailModel{}).Update(keyPress("enter"))
	if cmd == nil || !model.(detailModel).closing || model.(detailModel).quit {
		t.Error("enter did not navigate back cleanly")
	}
	model, cmd = (detailModel{}).Update(keyPress("esc"))
	if cmd == nil || !model.(detailModel).closing || model.(detailModel).quit {
		t.Error("escape did not navigate back cleanly")
	}
	model, cmd = (detailModel{}).Update(keyPress("ctrl+c"))
	if cmd == nil || !model.(detailModel).quit {
		t.Error("ctrl+c did not quit")
	}
}

// A read-only view still has to be able to act on what it reports — the status
// screen that knows the endpoint is unreachable is the one that must restart it.
func TestDetailActionKeysExitWithTheKey(t *testing.T) {
	d := Detail{Keys: []Key{{Key: "s", Help: "restart service"}}}

	model, cmd := (detailModel{Detail: d}).Update(keyPress("s"))
	if cmd == nil || model.(detailModel).key != "s" || !model.(detailModel).closing {
		t.Errorf("s did not exit as an action key: %+v", model)
	}

	// An unbound key is inert, and h stays the help toggle.
	model, cmd = (detailModel{Detail: d}).Update(keyPress("x"))
	if cmd != nil || model.(detailModel).key != "" {
		t.Error("an unbound key exited the view")
	}
	if view := (detailModel{Detail: d}).View().Content; !strings.Contains(view, "s restart service") {
		t.Errorf("the footer does not advertise the action key:\n%s", view)
	}
}
