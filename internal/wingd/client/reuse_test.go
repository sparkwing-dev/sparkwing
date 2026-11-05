package client

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestCompatibleDaemonIsReusedAcrossVersions(t *testing.T) {
	for _, versions := range []struct{ client, daemon string }{
		{"v0.69.2", "v0.68.3"},
		{"v0.68.3", "v0.69.2"},
		{"v0.69.2-dev+1b9e5cd9", "v0.69.2"},
		{"v0.69.2", "v0.69.2-dev+1b9e5cd9"},
		{"(devel)", "v0.69.2"},
		{"v0.69.2", "(devel)"},
	} {
		t.Run(versions.client+"/"+versions.daemon, func(t *testing.T) {
			home := shortHome(t)
			requests := oneShotDaemon(t, home, wingwire.HelloAck{
				ProtocolMajor: wingd.ProtocolMajor, BinaryVersion: versions.daemon,
				BuildIdentity: wingwire.BuildIdentity,
			})
			spawns := 0
			cl, err := EnsureDaemon(t.Context(), Options{
				Home: home, Version: versions.client,
				Spawn: func(string, string) error { spawns++; return errors.New("unexpected daemon spawn") },
			})
			if err != nil {
				t.Fatalf("compatible daemon rejected: %v (spawns=%d)", err, spawns)
			}
			if err := cl.Close(); err != nil {
				t.Fatal(err)
			}
			if request := <-requests; request != "" {
				t.Fatalf("connection sent %T instead of reusing daemon", request)
			}
			if spawns != 0 || cl.DaemonVersion() != versions.daemon {
				t.Fatalf("daemon changed: spawns=%d version=%q", spawns, cl.DaemonVersion())
			}
		})
	}
}

func TestIncompatibleDaemonIsRefusedWithoutDrainOrSpawn(t *testing.T) {
	for _, noTakeover := range []bool{false, true} {
		for _, test := range []struct {
			name     string
			version  string
			protocol int
			identity string
			want     error
		}{
			{"older protocol", "v0.68.3", wingd.ProtocolMajor - 1, wingwire.BuildIdentity, errDaemonTooOld},
			{"newer protocol", "v0.70.0", wingd.ProtocolMajor + 1, wingwire.BuildIdentity, errProtocolTooOld},
			{"older same-version build", "v0.69.2", wingd.ProtocolMajor, "wingwire-v1-liveness", errBuildMismatch},
			{"missing same-version build", "v0.69.2", wingd.ProtocolMajor, "", errBuildMismatch},
		} {
			t.Run(fmt.Sprintf("%s/no-self-host-%t", test.name, noTakeover), func(t *testing.T) {
				home := shortHome(t)
				requests := oneShotDaemon(t, home, wingwire.HelloAck{
					ProtocolMajor: test.protocol, BinaryVersion: test.version, BuildIdentity: test.identity,
				})
				spawns := 0
				_, err := EnsureDaemon(t.Context(), Options{
					Home: home, Version: "v0.69.2", NoTakeover: noTakeover,
					Spawn: func(string, string) error { spawns++; return errors.New("unexpected daemon spawn") },
				})
				if !errors.Is(err, test.want) || spawns != 0 {
					t.Fatalf("mismatch: error=%v spawns=%d, want %v without spawn", err, spawns, test.want)
				}
				if request := <-requests; request != "" {
					t.Fatalf("refused client sent %T", request)
				}
				for _, want := range []string{test.version, "v0.69.2"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q omits %q", err, want)
					}
				}
				if errors.Is(test.want, errDaemonTooOld) {
					if !strings.Contains(err.Error(), "sparkwing update") {
						t.Errorf("error %q omits update remedy", err)
					}
				}
				if !errors.Is(test.want, errProtocolTooOld) {
					for _, want := range []string{"v0.69.2", "sparkwing daemon restart --force", "when no runs are active"} {
						if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
							t.Errorf("error %q omits %q", err, want)
						}
					}
				}
			})
		}
	}
}
