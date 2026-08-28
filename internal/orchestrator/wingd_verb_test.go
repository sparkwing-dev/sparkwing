package orchestrator

import (
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestRunWingdServesEveryVerbASpawnInvokes(t *testing.T) {
	for _, verb := range []string{"run", wingdclient.DaemonSpawnVerb} {
		err := RunWingd([]string{verb, "--this-flag-does-not-exist"})
		if err == nil {
			t.Errorf("wingd %s --this-flag-does-not-exist succeeded; the probe must not run the verb", verb)
			continue
		}
		if strings.Contains(err.Error(), "usage: wingd") {
			t.Errorf("a daemon-hosting binary does not serve `wingd %s`, which the client's spawn invokes: %v", verb, err)
		}
	}

	if err := RunWingd([]string{"not-a-verb"}); err == nil || !strings.Contains(err.Error(), "usage: wingd") {
		t.Fatalf("the probe cannot tell served from unserved: an unknown verb gave %v", err)
	}
}
