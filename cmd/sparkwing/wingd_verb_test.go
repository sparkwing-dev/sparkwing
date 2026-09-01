package main

import (
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

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
