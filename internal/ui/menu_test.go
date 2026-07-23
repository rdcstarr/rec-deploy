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

// TestRunWizardSkipsAndRenumbers pins that a skipped step neither runs nor
// counts: a host without systemd has six steps, and numbering it "7" would
// promise a step the operator never reaches.
func TestRunWizardSkipsAndRenumbers(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })

	var order []string
	out := captureStdout(t, func() {
		if err := RunWizard(
			WizardStep{Name: "First", Run: func() error { order = append(order, "first"); return nil }},
			WizardStep{Name: "Skipped", Skip: func() bool { return true }, Run: func() error { order = append(order, "skipped"); return nil }},
			WizardStep{Name: "Second", Run: func() error { order = append(order, "second"); return nil }},
		); err != nil {
			t.Errorf("RunWizard: %v", err)
		}
	})

	if strings.Join(order, ",") != "first,second" {
		t.Errorf("skipped step ran: %v", order)
	}
	for _, want := range []string{"[1/2] First", "[2/2] Second"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing heading %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Skipped") {
		t.Errorf("skipped step was announced:\n%s", out)
	}
}

// TestStepIsTheOnlyBlankLine pins the wizard's spacing contract: a heading
// brings exactly one blank line with it, and nothing else in a wizard emits
// one. That is what makes the spacing uniform instead of incidental.
func TestStepIsTheOnlyBlankLine(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })

	out := captureStdout(t, func() { Step(2, 7, "Server") })
	if out != "\n[2/7] Server\n" {
		t.Errorf("Step printed %q, want \"\\n[2/7] Server\\n\"", out)
	}
}

// TestKeyValueColumnFitsEveryKey guards the alignment the summary and status
// panes depend on: a key wider than the column pushes its value a column right
// of every other row, which is what "auto-update" used to do.
func TestKeyValueColumnFitsEveryKey(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })

	short := KeyValueLine("state", "ready")
	long := KeyValueLine("auto-update", "on")
	if strings.Index(short, "ready") != strings.Index(long, "on") {
		t.Errorf("values are not aligned:\n%q\n%q", short, long)
	}
}

func TestDocumentPreservesPreformattedBody(t *testing.T) {
	body := "{\n  \"url\": \"https://example.com/a/very/long/path\"\n}"
	view := (documentModel{Document: Document{Title: "JSON", Body: body}}).View()
	if !strings.Contains(view, body) {
		t.Fatalf("document changed its body:\n%s", view)
	}
}

// TestRunWizardSurvivesAnOptionalStepFailure is the rule that a failing extra
// must not abandon setup. A Cloudflare MCP step that could not provision took
// notifications, auto-update and the summary down with it, and made the wizard
// exit non-zero — which is what an installer reads to decide whether to enable
// the daemon at all, so one optional feature failing left the server not
// running.
func TestRunWizardSurvivesAnOptionalStepFailure(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })

	var order []string
	out := captureStdout(t, func() {
		if err := RunWizard(
			WizardStep{Name: "Required", Run: func() error { order = append(order, "required"); return nil }},
			WizardStep{Name: "Extra", Optional: true, Run: func() error {
				order = append(order, "extra")

				return errors.New("dns record already exists")
			}},
			WizardStep{Name: "After", Run: func() error { order = append(order, "after"); return nil }},
		); err != nil {
			t.Errorf("an optional step's failure ended the wizard: %v", err)
		}
	})

	if strings.Join(order, ",") != "required,extra,after" {
		t.Errorf("steps after the failing extra did not run: %v", order)
	}
	if !strings.Contains(out, "dns record already exists") {
		t.Errorf("the failure was swallowed instead of reported:\n%s", out)
	}
}

// TestRunWizardStillStopsForRequiredStepsAndQuits pins the two cases Optional
// must not swallow: a required step's failure, and a quit from anywhere.
func TestRunWizardStillStopsForRequiredStepsAndQuits(t *testing.T) {
	SetColor(false)
	t.Cleanup(func() { SetColor(true) })

	wantErr := errors.New("no token")
	var reached bool
	_ = captureStdout(t, func() {
		err := RunWizard(
			WizardStep{Name: "Required", Run: func() error { return wantErr }},
			WizardStep{Name: "After", Run: func() error { reached = true; return nil }},
		)
		if !errors.Is(err, wantErr) {
			t.Errorf("a required step's failure did not end the wizard: %v", err)
		}
	})
	if reached {
		t.Error("a step after a failed required step ran")
	}

	reached = false
	_ = captureStdout(t, func() {
		err := RunWizard(
			WizardStep{Name: "Extra", Optional: true, Run: func() error { return ErrQuit }},
			WizardStep{Name: "After", Run: func() error { reached = true; return nil }},
		)
		if !IsQuit(err) {
			t.Errorf("a quit from an optional step was swallowed: %v", err)
		}
	})
	if reached {
		t.Error("a step after a quit ran")
	}
}
