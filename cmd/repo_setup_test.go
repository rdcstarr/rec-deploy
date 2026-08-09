package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/discover"
)

// TestLocalCheckoutsDegradesWhenDiscoveryAnswersNothing pins that discovery is an
// offer here, never a requirement. A scan that finds nothing — or fails outright
// on a machine with no discovery roots — narrows what the confirmation can
// describe instead of ending the command; the engine discovers again for itself.
func TestLocalCheckoutsDegradesWhenDiscoveryAnswersNothing(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg = &config.Config{}
	cfg.Discovery.Roots = []string{filepath.Join(t.TempDir(), "nothing-here")}

	if got := localCheckouts(context.Background(), "o/r"); len(got) != 0 {
		t.Errorf("localCheckouts = %+v, want none", got)
	}
}

// TestSetupCmdIsLocalOnly pins what v0.16.1 removed. `repo setup` used to offer a
// fleet arm that asked GitHub to deliver a repository_dispatch to every server
// registered on the repository — a transport that does not exist for repository
// webhooks, and whose mere presence in the events array took `repo add` down
// with a 422. What is left runs on this server, so there is no branch to choose
// across machines and no --branch flag to choose it with.
func TestSetupCmdIsLocalOnly(t *testing.T) {
	cmd := newRepoSetupCmd()

	if cmd.Flags().Lookup("branch") != nil {
		t.Error("--branch is still registered — it only ever narrowed a fleet-wide dispatch")
	}
	if strings.Contains(cmd.Long, "every server") {
		t.Errorf("Long still claims a fleet-wide reach: %q", cmd.Long)
	}
	if !strings.Contains(cmd.Long, "this server") {
		t.Errorf("Long does not say where setup runs: %q", cmd.Long)
	}
	if !strings.Contains(cmd.Long, "--yes") {
		t.Errorf("Long does not say how to run it unattended: %q", cmd.Long)
	}
}

// TestRunLocalSetupRefusesWithoutYesOutsideATerminal covers the scripted form,
// which the fleet arm used to swallow: every non-interactive run went to the
// dispatch, so the local arm was reachable from the interactive menu alone.
// Now it is the only arm, and it owes the same guard as every other destructive
// action — setup steps are frequently not idempotent. Tests have no TTY, so
// isInteractive() is false here.
func TestRunLocalSetupRefusesWithoutYesOutsideATerminal(t *testing.T) {
	saved := flagYes
	defer func() { flagYes = saved }()
	flagYes = false

	err := runLocalSetup(context.Background(), "o/r")
	if err == nil {
		t.Fatal("runLocalSetup outside a terminal without --yes = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error = %q, want the re-run hint in backticks", err)
	}
}

func TestRepoMenuOffersSetup(t *testing.T) {
	for _, o := range repoMenuOptions() {
		if o.Value == "setup" {
			return
		}
	}
	t.Error("the repo hub does not offer setup")
}

// TestDescribeLocalSetupNamesEveryCheckoutAndItsBranch pins what the confirmation
// owes before it runs. "This server" hides that a box holding staging on develop
// and production on main will run the setup steps on production too, so the
// prompt names which trees it will hit and the branch each is on — and points at
// the flag that narrows further, since setup deliberately grows no branch
// plumbing of its own.
func TestDescribeLocalSetupNamesEveryCheckoutAndItsBranch(t *testing.T) {
	got := describeLocalSetup([]discover.Installation{
		{Path: "/var/www/prod", Branch: "main"},
		{Path: "/var/www/staging", Branch: "develop"},
	}, "o/r")

	for _, want := range []string{"/var/www/prod", "main", "/var/www/staging", "develop"} {
		if !strings.Contains(got, want) {
			t.Errorf("describeLocalSetup = %q, want it to name %q", got, want)
		}
	}
	if !strings.Contains(got, "--path") {
		t.Errorf("describeLocalSetup = %q, want the escape hatch that narrows further", got)
	}
}

// Discovery is an offer in this command, never a requirement — see
// TestLocalCheckoutsDegradesWhenDiscoveryAnswersNothing. A scan that answers
// nothing must still produce a confirmation that says what will run, rather than
// an empty prompt that reads as "nothing will happen".
func TestDescribeLocalSetupStaysHonestWithNoCheckouts(t *testing.T) {
	got := describeLocalSetup(nil, "o/r")

	if !strings.Contains(got, "o/r") || !strings.Contains(got, "this server") {
		t.Errorf("describeLocalSetup(nil) = %q, want it to still say what the run covers", got)
	}
}
