package ui

import (
	"strings"
	"testing"
)

func TestSecretDetailRevealIsReadOnlyAndScoped(t *testing.T) {
	const secret = "rdmcp_secret"
	model := secretDetailModel{SecretDetail: SecretDetail{Title: "Token", Label: "bearer token", Value: secret}}
	if view := model.View(); strings.Contains(view, secret) || !strings.Contains(view, "********") {
		t.Fatalf("secret detail did not start masked:\n%s", view)
	}
	next, _ := model.Update(keyPress("alt+r"))
	model = next.(secretDetailModel)
	if view := model.View(); !strings.Contains(view, secret) || !strings.Contains(view, "mask") {
		t.Errorf("Alt+R did not reveal the secret:\n%s", view)
	}
	next, _ = model.Update(keyPress("alt+r"))
	if view := next.(secretDetailModel).View(); strings.Contains(view, secret) {
		t.Errorf("second Alt+R did not mask the secret:\n%s", view)
	}
}
