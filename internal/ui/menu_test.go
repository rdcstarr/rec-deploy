package ui

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestDescribedOptionsAlignDescriptions(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })
	options := DescribedOptions(
		DescribedOption{Name: "A", Description: "first", Value: "a"},
		DescribedOption{Name: "Long name", Description: "second", Value: "b"},
	)
	if strings.Index(options[0].Label, "first") != strings.Index(options[1].Label, "second") {
		t.Fatalf("descriptions are not aligned: %#v", options)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = original }()
	fn()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestReportPrintUsesSharedRows(t *testing.T) {
	out := captureStdout(t, func() {
		(Report{Title: "Status", Rows: [][2]string{{"state", "ready"}}}).Print()
	})
	if !strings.Contains(out, "Status") || !strings.Contains(out, "state") || !strings.Contains(out, "ready") {
		t.Fatalf("unexpected report output: %q", out)
	}
}

func TestRunWizardPreservesOrderAndStopsOnError(t *testing.T) {
	var order []string
	wantErr := errors.New("stop")
	err := RunWizard(
		WizardStep{Name: "one", Run: func() error { order = append(order, "one"); return nil }},
		WizardStep{Name: "two", Run: func() error { order = append(order, "two"); return wantErr }},
		WizardStep{Name: "three", Run: func() error { order = append(order, "three"); return nil }},
	)
	if !errors.Is(err, wantErr) || strings.Join(order, ",") != "one,two" {
		t.Fatalf("RunWizard order=%v err=%v", order, err)
	}
}

func TestDocumentPreservesPreformattedBody(t *testing.T) {
	body := "{\n  \"url\": \"https://example.com/a/very/long/path\"\n}"
	view := (documentModel{Document: Document{Title: "JSON", Body: body}}).View()
	if !strings.Contains(view, body) {
		t.Fatalf("document changed its body:\n%s", view)
	}
}
