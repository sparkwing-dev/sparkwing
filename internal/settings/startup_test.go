package settings

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestRunnerPreferencesMergeAndRejectInvalidRenewals(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true}, Flags: Values{RunnerPreferences: &userconfig.RunnerConfig{MaxConcurrent: pointer(3)}}, Machine: Values{RunnerPreferences: &userconfig.RunnerConfig{Labels: []string{"gpu"}}}}
	runner, _, err := RunnerPreferences(in)
	if err != nil || *runner.MaxConcurrent != 3 || len(runner.Labels) != 1 {
		t.Fatal(runner, err)
	}
	runner.Labels[0] = "changed"
	if in.Machine.RunnerPreferences.Labels[0] != "gpu" {
		t.Fatal("runner aliases config")
	}
	in.Flags.RunnerPreferences = &userconfig.RunnerConfig{Lease: pointer("1m"), Heartbeat: pointer("30s")}
	if _, _, err := RunnerPreferences(in); err == nil {
		t.Fatal("two renewals not strictly before expiry")
	}
}

func TestParentCheckoutPrecedesMachineLocation(t *testing.T) {
	in := Inputs{Context: Context{Submitter: true, ParentCheckout: "/parent"}, Machine: Values{Options: runspec.Options{RepoLocation: present("/registered")}}}
	value, origin, err := RepoLocation(in)
	if err != nil || value != "/parent" || origin.Source != Run {
		t.Fatalf("%q %+v %v", value, origin, err)
	}
	in.Flags.RepoLocation = present("/submitted")
	value, _, err = RepoLocation(in)
	if err != nil || value != "/submitted" {
		t.Fatalf("%q %v", value, err)
	}
}
