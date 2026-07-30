package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestSecretDetailRevealIsReadOnlyAndScoped(t *testing.T) {
	const secret = "rdmcp_secret"
	model := secretDetailModel{SecretDetail: SecretDetail{Title: "Token", Label: "bearer token", Value: secret}}
	if view := model.View().Content; strings.Contains(view, secret) || !strings.Contains(view, "********") {
		t.Fatalf("secret detail did not start masked:\n%s", view)
	}
	next, _ := model.Update(keyPress("alt+r"))
	model = next.(secretDetailModel)
	if view := model.View().Content; !strings.Contains(view, secret) || !strings.Contains(view, "mask") {
		t.Errorf("Alt+R did not reveal the secret:\n%s", view)
	}
	next, _ = model.Update(keyPress("alt+r"))
	if view := next.(secretDetailModel).View().Content; strings.Contains(view, secret) {
		t.Errorf("second Alt+R did not mask the secret:\n%s", view)
	}
}

// TestSecretDetailFrameFitsTheTerminal pins the fourth view that renders an
// assembled frame. It is six rows whatever the terminal does — one value has
// nothing to window — so it never needed the fitting arithmetic the other three
// grew, and it was the one place the busy-loop survived them: measured on a
// 4-row pty, 180 full-screen clears and 37KB in three idle seconds, against
// zero on Charm v1. clampFrame is the whole fix here, and cropping is the right
// answer, because below six rows the view has nothing left to give up.
func TestSecretDetailFrameFitsTheTerminal(t *testing.T) {
	SetColor(false)

	for height := 1; height <= 40; height++ {
		var m tea.Model = secretDetailModel{SecretDetail: SecretDetail{Title: "Token", Label: "bearer token", Value: "s"}}
		m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: height})

		if got := frameHeight(m.(secretDetailModel).View().Content); got > height {
			t.Fatalf("a %d-row terminal rendered a %d-row frame", height, got)
		}
	}
}
