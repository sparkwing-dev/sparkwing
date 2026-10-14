package main

import (
	"os"
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func hostBinValues(env []string) []string {
	var out []string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, wingdclient.HostBinEnv+"="); ok {
			out = append(out, v)
		}
	}
	return out
}

func TestWithWingdHost_NamesThisCLIAsTheDaemonHost(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "")
	self, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	got := hostBinValues(withWingdHost([]string{"PATH=/usr/bin"}))
	if len(got) != 1 || got[0] != self {
		t.Fatalf("%s = %v, want exactly [%s]", wingdclient.HostBinEnv, got, self)
	}
}

func TestWithWingdHost_OperatorExportSurvives(t *testing.T) {
	const operator = "/opt/custom/sparkwing"
	t.Setenv(wingdclient.HostBinEnv, operator)
	env := withWingdHost(append(os.Environ(), wingdclient.HostBinEnv+"="+operator))
	got := hostBinValues(env)
	if len(got) == 0 {
		t.Fatalf("%s was dropped from the child env", wingdclient.HostBinEnv)
	}

	if last := got[len(got)-1]; last != operator {
		t.Fatalf("child would see %s=%s, want the operator's %s", wingdclient.HostBinEnv, last, operator)
	}
}
