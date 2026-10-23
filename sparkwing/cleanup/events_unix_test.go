//go:build unix

package cleanup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
)

func TestOrphanCleanupDiagnosticsNeverUseExecutableText(t *testing.T) {
	for _, tc := range []struct {
		name, script, description string
		fails                     bool
	}{
		{"success", "#!/bin/sh\nexit 0\n", "cleanup helper", false},
		{"failure", "#!/bin/sh\nexit 3\n", "cleanup helper", true},
		{"missing", "", "cleanup helper", true},
		{"without description", "#!/bin/sh\nexit 0\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("SPARKWING_HOME", home)
			t.Setenv("SPARKWING_RUN_ID", "run-1")
			t.Setenv("SPARKWING_JOB_ID", "build")
			const secret = "private-cleanup-token"
			program := filepath.Join(home, "cleanup-"+secret)
			if tc.script != "" {
				if err := os.WriteFile(program, []byte(tc.script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			release, err := Register(t.Context(), Spec{Argv: []string{program, secret}, Description: tc.description})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ledger := sessionledger.Open(paths.PathsAt(home).SessionLedgerDir())
			records, err := ledger.List()
			if err != nil || len(records) != 1 {
				t.Fatalf("records = %+v, err = %v", records, err)
			}
			// A mismatched birth token represents an owner that has exited.
			records[0].OwnerBirth = "gone"
			if _, err := ledger.Record(records[0]); err != nil {
				t.Fatal(err)
			}
			outcomes, err := ledger.Sweep(t.Context(), sessionledger.SweepOptions{})
			if err != nil || len(outcomes) != 1 {
				t.Fatalf("outcomes = %+v, err = %v", outcomes, err)
			}
			if (outcomes[0].Err != nil) != tc.fails {
				t.Fatalf("cleanup error = %v, want failure = %t", outcomes[0].Err, tc.fails)
			}
			events := &cleanupEvents{}
			if errs := sessionledger.RecordOutcomes(t.Context(), events, "doctor", outcomes); len(errs) != 0 {
				t.Fatal(errs)
			}
			want := tc.description
			if want == "" {
				want = "registered cleanup"
			}
			if len(events.payloads) != 1 || strings.Contains(events.payloads[0], secret) || !strings.Contains(events.payloads[0], want) {
				t.Errorf("events = %q, want safe description %q without executable or argument text", events.payloads, want)
			}
			diagnostics, err := json.Marshal(outcomes)
			if err != nil || strings.Contains(string(diagnostics), secret) {
				t.Errorf("diagnostics = %s, err = %v, want no private replay text", diagnostics, err)
			}
			if outcomes[0].Err != nil && strings.Contains(outcomes[0].Err.Error(), secret) {
				t.Errorf("cleanup error = %v, want no executable text", outcomes[0].Err)
			}
		})
	}
}

type cleanupEvents struct{ payloads []string }

func (e *cleanupEvents) AppendEvent(_ context.Context, _, _, _ string, payload []byte) error {
	e.payloads = append(e.payloads, string(payload))
	return nil
}
