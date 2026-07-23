package cmd

import "testing"

// TestHubOptionsIncludesUninstall pins that the interactive hub now lists
// uninstall (its root check and confirmation wizard are the guard, not its
// absence from the menu) and still omits Cobra plumbing commands.
func TestHubOptionsIncludesUninstall(t *testing.T) {
	var found bool
	for _, o := range hubOptions(newRootCmd()) {
		if o.Value == "uninstall" {
			found = true
		}
		if o.Value == "help" || o.Value == "completion" {
			t.Errorf("hub lists cobra plumbing command %q", o.Value)
		}
	}
	if !found {
		t.Error("hub is missing the uninstall command")
	}
}
