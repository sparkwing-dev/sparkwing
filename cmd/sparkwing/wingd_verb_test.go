package main

import (
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

// TestWingdServesEveryVerbASpawnInvokes is the lockstep pin between the
// verb the client's spawn execs and the verbs this binary dispatches.
//
// It exists because that pair came apart once and broke every local run
// without a live daemon: the spawn moved to `supervise` (965f77d4) while
// compiled pipeline binaries -- which were then the spawning binary --
// served only `run`, so the exec failed and admission reported itself
// unreachable. The verb only returned once spawning moved to installed
// binaries, and nothing but a test stops it drifting again.
//
// The dispatch is probed with an unparseable flag rather than run: a
// served verb rejects the flag, an unserved one says it does not know the
// subcommand, and neither starts a daemon.
func TestWingdServesEveryVerbASpawnInvokes(t *testing.T) {
	verbs := []string{"run", wingdclient.DaemonSpawnVerb}
	for _, verb := range verbs {
		err := runWingd([]string{verb, "--this-flag-does-not-exist"})
		if err == nil {
			t.Errorf("wingd %s --this-flag-does-not-exist succeeded; the probe must not run the verb", verb)
			continue
		}
		if strings.Contains(err.Error(), "unknown subcommand") {
			t.Errorf("the sparkwing CLI does not serve `wingd %s`, which the client's spawn invokes: %v", verb, err)
		}
	}

	if err := runWingd([]string{"not-a-verb"}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("the probe cannot tell served from unserved: an unknown verb gave %v", err)
	}
}
