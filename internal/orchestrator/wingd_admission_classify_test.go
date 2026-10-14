package orchestrator

import (
	"errors"
	"strings"
	"testing"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func TestDaemonStoreSchemaSkewRefusesADaemonBehindTheStore(t *testing.T) {
	err := daemonStoreSchemaSkew("v0.38.2", "v0.39.0", 17, 26)
	if !errors.Is(err, ErrDaemonStoreSchemaTooOld) {
		t.Fatalf("skew error = %v, want ErrDaemonStoreSchemaTooOld", err)
	}
	for _, want := range []string{"v0.38.2", "v0.39.0", "17", "26", wingdclient.HostBinEnv} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("skew error = %q, want it to name %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "upgrade that binary to one that understands schema 26") {
		t.Fatalf("skew error = %q, want the restart offered only after the upgrade that changes the binary", err)
	}
}

func TestDaemonStoreSchemaSkewAcceptsOnlyAMatchingDaemon(t *testing.T) {
	if err := daemonStoreSchemaSkew("v0.39.0", "v0.39.0", 26, 26); err != nil {
		t.Fatalf("skew error = %v, want none for a matching daemon", err)
	}
	for _, tc := range []struct {
		name         string
		daemonSchema int
	}{
		{"daemon predates the field", 0},
		{"daemon is ahead", 27},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := daemonStoreSchemaSkew("v0.39.0", "v0.39.0", tc.daemonSchema, 26); !errors.Is(err, ErrDaemonStoreSchemaTooOld) {
				t.Fatalf("skew error = %v, want ErrDaemonStoreSchemaTooOld", err)
			}
		})
	}
}

func TestDaemonUpgradeRemedyWithoutAResolvableHostBinary(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "")
	t.Setenv("PATH", t.TempDir())
	got := daemonUpgradeRemedy(34)
	for _, want := range []string{
		"`sparkwing daemon status` names the build the daemon runs",
		"upgrade that binary to one that understands schema 34",
		"set " + wingdclient.HostBinEnv + " to that binary and restart the daemon",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("remedy = %q, want it to contain %q", got, want)
		}
	}
	if strings.Contains(got, "resolves to") {
		t.Errorf("remedy = %q, want no binary path when none resolves", got)
	}
}

func TestStoreSchemaSkewNamesTheDaemonHostAndTheUpgrade(t *testing.T) {
	t.Setenv(wingdclient.HostBinEnv, "/opt/sparkwing/bin/sparkwing")
	err := daemonStoreSchemaSkew("v0.38.2", "v0.39.0", 17, 26)
	for _, want := range []string{
		"daemon v0.38.2 understands runs-store schema 17",
		"this binary is v0.39.0 at schema 26",
		"$" + wingdclient.HostBinEnv + " resolves to /opt/sparkwing/bin/sparkwing",
		"with `sparkwing update` for a published release or this repository's `bin/install.sh`",
		"then `sparkwing daemon restart`",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("skew error = %q, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "home of its own") || strings.Contains(err.Error(), "isolated-home") {
		t.Errorf("skew error = %q, want no isolated-home remedy", err)
	}
}
