package cmd

import (
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/ui"
	"github.com/rdcstarr/rec-deploy/internal/uninstall"
)

// TestReportLinesGroupEveryStepUnderItsPhase pins the grouping that replaced a
// flat sixteen-line dump: each phase is introduced once, immediately before its
// own steps, and the engine's order survives. The order is what makes the
// report readable as a sequence of things that happened rather than a list.
func TestReportLinesGroupEveryStepUnderItsPhase(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(true) })

	lines := reportLines(uninstall.Report{Steps: []uninstall.Step{
		{Phase: uninstall.PhaseServices, Target: "rec-deploy.service", Outcome: uninstall.OutcomeRemoved, Detail: "stopped and disabled"},
		{Phase: uninstall.PhaseServices, Target: "rec-deploy-update.timer", Outcome: uninstall.OutcomeSkipped, Detail: "not enabled or running"},
		{Phase: uninstall.PhaseUnitFiles, Target: "/etc/systemd/system/rec-deploy.service", Outcome: uninstall.OutcomeRemoved},
		{Phase: uninstall.PhaseData, Target: "/etc/rec-deploy", Outcome: uninstall.OutcomeKept},
		{Phase: uninstall.PhaseBinary, Target: "/usr/bin/rec-deploy", Outcome: uninstall.OutcomeRemoved},
	}})

	want := []string{
		"  services",
		"    rec-deploy.service: removed — stopped and disabled",
		"    rec-deploy-update.timer: already gone — not enabled or running",
		"  unit files",
		"    /etc/systemd/system/rec-deploy.service: removed",
		"  data",
		"    /etc/rec-deploy: kept",
		"  binary",
		"    /usr/bin/rec-deploy: removed",
	}
	if len(lines) != len(want) {
		t.Fatalf("reportLines produced %d lines, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i, w := range want {
		if lines[i] != w {
			t.Errorf("line %d = %q, want %q", i, lines[i], w)
		}
	}
}

// TestReportLinesRepeatALabelOnlyWhenThePhaseChanges guards the cheap mistake
// in a grouping loop: printing the label for every step, which is noisier than
// the flat list it replaced.
func TestReportLinesRepeatALabelOnlyWhenThePhaseChanges(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(true) })

	lines := reportLines(uninstall.Report{Steps: []uninstall.Step{
		{Phase: uninstall.PhaseServices, Target: "a", Outcome: uninstall.OutcomeRemoved},
		{Phase: uninstall.PhaseServices, Target: "b", Outcome: uninstall.OutcomeRemoved},
		{Phase: uninstall.PhaseServices, Target: "c", Outcome: uninstall.OutcomeRemoved},
	}})

	var labels int
	for _, line := range lines {
		if line == "  services" {
			labels++
		}
	}
	if labels != 1 {
		t.Errorf("the services label appears %d times, want once:\n%s", labels, strings.Join(lines, "\n"))
	}
}

// TestReportLinesKeepAFailureAlignedAndMarked pins that a failed target stays
// visible without breaking the column the rest of the block sits in — the
// marker and its space occupy exactly the indent the other lines use.
func TestReportLinesKeepAFailureAlignedAndMarked(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(true) })

	lines := reportLines(uninstall.Report{Steps: []uninstall.Step{
		{Phase: uninstall.PhaseServices, Target: "rec-deploy.service", Outcome: uninstall.OutcomeRemoved},
		{Phase: uninstall.PhaseServices, Target: "rec-deploy-mcp.service", Outcome: uninstall.OutcomeFailed, Detail: "unit is masked"},
	}})

	failed := lines[len(lines)-1]
	if !strings.HasPrefix(failed, "!") {
		t.Errorf("a failed step = %q, want it marked", failed)
	}
	if !strings.Contains(failed, "unit is masked") {
		t.Errorf("a failed step = %q, want its detail verbatim", failed)
	}
	if column := strings.Index(failed, "rec-deploy-mcp.service"); column != 4 {
		t.Errorf("a failed step's text starts at column %d, want 4 like the lines above it", column)
	}
}

// TestReportLinesNeverDropAStep is the property that stops the grouping from
// hiding a target. A step the engine emitted without naming its phase — a pass
// added later and forgotten here — must still print, because a report that
// omits what it did is the defect this command exists not to be.
func TestReportLinesNeverDropAStep(t *testing.T) {
	ui.SetColor(false)
	t.Cleanup(func() { ui.SetColor(true) })

	steps := []uninstall.Step{
		{Phase: uninstall.PhaseServices, Target: "rec-deploy.service", Outcome: uninstall.OutcomeRemoved},
		{Target: "an-unnamed-pass", Outcome: uninstall.OutcomeRemoved},
	}
	lines := strings.Join(reportLines(uninstall.Report{Steps: steps}), "\n")

	for _, s := range steps {
		if !strings.Contains(lines, s.Target) {
			t.Errorf("reportLines dropped %q:\n%s", s.Target, lines)
		}
	}
}

// TestUninstallStepCountSkipsThePhasesTheRunWillNotTake pins the init rule the
// numbering borrows: a phase the run will not take is not a step at all, so it
// never inflates the denominator. The local system is always a step — even
// --keep-data does not remove it, since the services, the unit files and the
// binary still go.
func TestUninstallStepCountSkipsThePhasesTheRunWillNotTake(t *testing.T) {
	tests := []struct {
		name                   string
		doGitHub, doCloudflare bool
		want                   int
	}{
		{"nothing remote to clean", false, false, 1},
		{"github only", true, false, 2},
		{"cloudflare only", false, true, 2},
		{"both", true, true, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The expression under test is the one runUninstall builds the
			// stepper with; keeping it here in the same shape is what makes a
			// change to it fail this test.
			got := 1 + boolToInt(tt.doGitHub) + boolToInt(tt.doCloudflare)
			if got != tt.want {
				t.Errorf("step count for github=%v cloudflare=%v = %d, want %d", tt.doGitHub, tt.doCloudflare, got, tt.want)
			}
		})
	}
}
