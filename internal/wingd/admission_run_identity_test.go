package wingd

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestNodeHostAdmissionReadsItsRealRun(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	_, err = db.DB().ExecContext(t.Context(), `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days)
VALUES ('default','real-run',1,'example','running','{}','request',1,'gh:1','host','execute','[]','account',1,'r30',30)`)
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := db.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	d := configuredHandlerDaemon(t, Config{Runs: &FuncRunStore{IsTerminal: func(id string) (bool, error) {
		row, err := tenant.ReadRun(context.Background(), id)
		return row.State == store.RunSuccess || row.State == store.RunFailed || row.State == store.RunCancelled, err
	}}}, 4)
	parent, peer := handlerConn(t, d)
	grant := mustGrantFrame(t, callAndRead(t, peer, func() {
		d.handleAdmission(parent, &wingwire.AdmissionRequest{RunID: "real-run", SemaphoresOnly: true})
	}))
	ordinal := 0
	if err := quick.Check(func(node string) bool {
		node = fmt.Sprintf("%d/%s", ordinal, node)
		ordinal++
		host, peer := handlerConn(t, d)
		message := callAndRead(t, peer, func() {
			d.handleAdmission(host, &wingwire.AdmissionRequest{
				RunID: wingwire.NodeHostParticipant("real-run", node), OwnerRunID: "real-run", OwnerLeaseToken: grant.LeaseToken,
				SubLease: true, Resources: wingwire.HostResources{Cores: 1},
			})
		})
		held, admitted := message.(*wingwire.Grant)
		if !admitted {
			t.Logf("admission for node %q: %#v", node, message)
			return false
		}
		d.reattachWait[host.leaseID] = struct{}{}
		d.reattachMembers[host.leaseID] = []string{host.runID}
		reconnected, peer := handlerConn(t, d)
		message = callAndRead(t, peer, func() {
			d.handleReattach(reconnected, &wingwire.Reattach{LeaseToken: held.LeaseToken, RunID: host.runID})
		})
		_, admitted = message.(*wingwire.Grant)
		if !admitted {
			t.Logf("reattach for node %q: %#v", node, message)
		}
		d.handleRelease(reconnected, &wingwire.Release{LeaseToken: held.LeaseToken})
		return admitted
	}, &quick.Config{MaxCount: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB().ExecContext(t.Context(), `UPDATE runs SET status='failed',finished_at=1 WHERE id='real-run'`); err != nil {
		t.Fatal(err)
	}
	host, peer := handlerConn(t, d)
	message := callAndRead(t, peer, func() {
		d.handleAdmission(host, &wingwire.AdmissionRequest{RunID: wingwire.NodeHostParticipant("real-run", "after-finish"), SubLease: true})
	})
	if refusal, ok := message.(*wingwire.Evicted); !ok || refusal.Key != "cancelled" {
		t.Fatalf("terminal run admitted a node host: %#v", message)
	}
}
