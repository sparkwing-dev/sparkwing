//go:build unix

package opsview

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestSessionSweepRecordingToleratesPrunedRun(t *testing.T) {
	p := paths.PathsAt(t.TempDir())
	st, err := teststore.Open(p.StateDB())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.DB().ExecContext(t.Context(), `INSERT INTO runs
		(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
		VALUES ('default','pruned',1,'build','pending','{}','request',1,'github.com/acme/repo','host','execute','[]','member',1,'default',1)`); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("true")
	if err := owner.Run(); err != nil {
		t.Fatal(err)
	}
	ledger := sessionledger.Open(p.SessionLedgerDir())
	if _, err := ledger.Record(sessionledger.Record{
		Run: "pruned", Node: "build", OwnerPID: owner.Process.Pid, OwnerBirth: "gone",
		Handle: sessionledger.Handle{Kind: "command", ID: "cleanup", Argv: []string{"true"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(t.Context(), "DELETE FROM runs WHERE team='default' AND id='pruned'"); err != nil {
		t.Fatal(err)
	}
	var report DoctorReport
	if err := diagnoseStraySessions(t.Context(), p, st, false, &report); err != nil {
		t.Fatalf("successful sweep failed on outcome recording: %v", err)
	}
	if len(report.StrayStepSessions) != 1 || report.StrayStepSessions[0].Verdict != string(sessionledger.VerdictReaped) {
		t.Fatalf("sweep result: %+v", report.StrayStepSessions)
	}
	if left, err := ledger.List(); err != nil || len(left) != 0 {
		t.Fatalf("sweep left records: %+v %v", left, err)
	}
	for _, format := range []string{"pretty", "plain", "json"} {
		var output bytes.Buffer
		if err := RenderDoctor(&output, report, format); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"pretty": "warning: could not record", "plain": "unrecorded_step_outcomes\t1", "json": `"record_error"`}[format]
		if !strings.Contains(output.String(), want) {
			t.Fatalf("%s output hides unrecorded outcome: %s", format, output.String())
		}
	}
}
