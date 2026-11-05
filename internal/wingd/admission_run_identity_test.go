package wingd

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestNodeHostAdmissionReadsItsRealRun(t *testing.T) {
	db, err := teststore.OpenExisting(t, filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = teststore.SQL(t, db).ExecContext(t.Context(), `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,created_principal,created_at,log_retention_class,log_retention_days)
VALUES ('default','real-run',1,'example','running',?,'request',1,'gh:1','host','execute','account',1,'r30',30)`, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := db.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	d := configuredHandlerDaemon(t, Config{Runs: &FuncRunStore{IsTerminal: func(id string) (bool, error) {
		row, err := tenant.ReadRun(context.Background(), id)
		return row.State == runstate.RunSuccess || row.State == runstate.RunFailed || row.State == runstate.RunCancelled, err
	}}}, 4)
	parent := policyRequest(t, d, wingwire.AdmissionRequest{RunID: "real-run", SemaphoresOnly: true})
	if parent.Grant == nil {
		t.Fatalf("parent = %+v", parent)
	}
	ordinal := 0
	if err := quick.Check(func(node string) bool {
		node = fmt.Sprintf("%d/%s", ordinal, node)
		ordinal++
		state := policyRequest(t, d, wingwire.AdmissionRequest{RunID: wingwire.NodeHostParticipant("real-run", node), OwnerRunID: "real-run", OwnerLeaseToken: parent.Grant.LeaseToken, SubLease: true, Resources: wingwire.HostResources{Cores: 1}})
		if state.Grant == nil {
			t.Logf("admission for node %q: %+v", node, state)
			return false
		}
		policyRelease(t, d, state.ID)
		return true
	}, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := teststore.SQL(t, db).ExecContext(t.Context(), `UPDATE runs SET status='failed',finished_at=1 WHERE id='real-run'`); err != nil {
		t.Fatal(err)
	}
	state := policyRequest(t, d, wingwire.AdmissionRequest{RunID: wingwire.NodeHostParticipant("real-run", "after-finish"), SubLease: true})
	if state.Evicted == nil || state.Evicted.Key != "cancelled" {
		t.Fatalf("terminal run admitted a node host: %+v", state)
	}
}
