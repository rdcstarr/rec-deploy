package cmd

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rdcstarr/rec-deploy/internal/config"
	"github.com/rdcstarr/rec-deploy/internal/discover"
	"github.com/rdcstarr/rec-deploy/internal/github"
)

func TestDispatchReachCountsSubscribedHooks(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}, URL: "http://1.2.3.4:9000/hook/a"},
		{ID: 2, Active: true, Events: []string{"push"}, URL: "http://5.6.7.8:9000/hook/b"},
		{ID: 3, Active: false, Events: []string{"push", "repository_dispatch"}, URL: "http://9.9.9.9:9000/hook/c"},
	}

	ready, stale := dispatchReach(hooks)
	if ready != 1 {
		t.Errorf("ready = %d, want 1 — an inactive hook delivers nothing", ready)
	}
	if stale != 2 {
		t.Errorf("stale = %d, want 2", stale)
	}
}

// TestDispatchReachCountsOnlyRecDeployWebhooks is the guard the reachability
// check exists for. A repository that no rec-deploy server is registered on can
// still carry webhooks — a Slack app, a CI endpoint, a hook subscribed to
// everything with ["*"] — and counting those as servers turns the dispatch into
// a success report for work that will happen nowhere. They are also not
// repairable: there is no rec-deploy server behind a Slack hook to run
// `repo check --repair` on.
func TestDispatchReachCountsOnlyRecDeployWebhooks(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}, URL: "https://hooks.slack.com/services/T00/B00/xyz"},
		{ID: 2, Active: true, Events: []string{"*"}, URL: "https://ci.example.com/github"},
		{ID: 3, Active: true, Events: []string{"push"}, URL: "https://example.com/hooks/github"},
	}

	if ready, stale := dispatchReach(hooks); ready != 0 || stale != 0 {
		t.Errorf("reach = %d/%d over foreign webhooks alone, want 0/0", ready, stale)
	}
}

// TestDispatchReachRecognisesARecDeployWebhookUnderAPath covers a public_url
// with a base path — https://host/rec-deploy — which github.HookURL appends
// /hook/<token> to like any other.
func TestDispatchReachRecognisesARecDeployWebhookUnderAPath(t *testing.T) {
	hooks := []github.Hook{
		{ID: 1, Active: true, Events: []string{"push", "repository_dispatch"}, URL: "https://example.com/rec-deploy/hook/tok"},
		{ID: 2, Active: true, Events: []string{"*"}, URL: "http://5.6.7.8:9000/hook/tok2"},
	}

	if ready, stale := dispatchReach(hooks); ready != 2 || stale != 0 {
		t.Errorf("reach = %d/%d, want 2/0", ready, stale)
	}
}

func TestDispatchReachOnNoHooks(t *testing.T) {
	if ready, stale := dispatchReach(nil); ready != 0 || stale != 0 {
		t.Errorf("reach = %d/%d, want 0/0", ready, stale)
	}
}

// TestSetupBranchOptionsOffersEveryBranchAndThisServersOwn covers the choice
// that mitigates the sharpest edge in this feature: a dispatch reaches every
// server registered on the repository, and install steps are frequently not
// idempotent, so an operator who cannot narrow it has only the destructive
// option. This server's own checkouts are not the fleet's answer — no server
// knows what the others hold — but they are the honest set to offer.
func TestSetupBranchOptionsOffersEveryBranchAndThisServersOwn(t *testing.T) {
	options := setupBranchOptions([]discover.Installation{
		{Path: "/var/www/prod", Branch: "main"},
		{Path: "/var/www/staging", Branch: "develop"},
		{Path: "/var/www/second", Branch: "main"},
		{Path: "/var/www/detached", Branch: ""},
	})

	var values []string
	for _, o := range options {
		values = append(values, o.Value)
	}

	want := []string{branchEvery, "develop", "main", branchOther}
	if !slices.Equal(values, want) {
		t.Errorf("options = %v, want %v — every branch, each distinct local branch once, then a hand-typed one", values, want)
	}
}

// TestSetupBranchOptionsStayUsableWithNoLocalCheckouts is the laptop case, and
// the reason the hand-typed entry exists: `repo setup` requires no local state by
// design, so discovery routinely finds nothing here. Narrowing must still be
// reachable — offering only "every branch" would make the widest blast radius
// the sole option again.
func TestSetupBranchOptionsStayUsableWithNoLocalCheckouts(t *testing.T) {
	options := setupBranchOptions(nil)

	if len(options) != 2 || options[0].Value != branchEvery || options[1].Value != branchOther {
		t.Fatalf("options = %+v, want every branch and a hand-typed one", options)
	}
}

// TestLocalCheckoutsDegradesWhenDiscoveryAnswersNothing pins the other half of
// that: discovery is an offer here, never a requirement. A scan that finds
// nothing — or fails outright on a machine with no discovery roots — narrows
// what can be offered instead of ending a command documented to need no local
// state at all.
func TestLocalCheckoutsDegradesWhenDiscoveryAnswersNothing(t *testing.T) {
	saved := cfg
	defer func() { cfg = saved }()

	cfg = &config.Config{}
	cfg.Discovery.Roots = []string{filepath.Join(t.TempDir(), "nothing-here")}

	if got := localCheckouts(context.Background(), "o/r"); len(got) != 0 {
		t.Errorf("localCheckouts = %+v, want none", got)
	}
}

func TestSetupCmdFlags(t *testing.T) {
	cmd := newRepoSetupCmd()

	if cmd.Flags().Lookup("branch") == nil {
		t.Error("--branch is not registered")
	}
	if !strings.Contains(cmd.Long, "every server") {
		t.Errorf("Long does not say the dispatch reaches every server: %q", cmd.Long)
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

// TestDescribeLocalSetupNamesEveryCheckoutAndItsBranch pins what the local arm
// owes before it runs. "This server" reached the engine with no branch question
// and no confirmation, so a box holding staging on develop and production on
// main ran the setup steps on production from two menu picks. The confirmation
// has to show which trees it will hit and the branch each is on — and point at
// the flag that narrows further, since the local arm deliberately grows no
// branch plumbing of its own.
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

// TestServerCountSaysWhenTheListingWasCutShort is the other half of the page
// fix. Hooks reads one page of github.HooksPerPage, so a full page back means
// GitHub had more to give and the count is a floor. Printed bare it would state
// a number that is simply wrong, on the one line an operator uses to decide
// whether a fleet-wide dispatch is going where they think.
func TestServerCountSaysWhenTheListingWasCutShort(t *testing.T) {
	if got := serverCount(3, false); got != plural(3, "server") {
		t.Errorf("serverCount(3, false) = %q, want the plain count", got)
	}

	got := serverCount(github.HooksPerPage, true)
	if !strings.Contains(got, "at least") {
		t.Errorf("serverCount(full page) = %q, want it to read as a floor", got)
	}
}
