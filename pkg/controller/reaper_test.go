package controller

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

func TestReaperRecoversExpiredOwnerAndKeepsLiveOwner(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "expired"}[expired], func(t *testing.T) {
			st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := t.Context()
			if _, err := st.DB().ExecContext(ctx, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES (?,'run',1,'p','running',?,'request',1,'gh:1','host','execute',?,'account',1,'r30',30)`, store.DefaultTeam, []byte(`{"version":1,"request":{"origin":"trigger"}}`), []byte(`[]`)); err != nil {
				t.Fatal(err)
			}
			until := time.Now().Add(time.Hour).UnixNano()
			if expired {
				until = 1
			}
			if _, err := st.DB().ExecContext(ctx, `INSERT INTO triggers(team,id,status,unit_kind,available_at,created_at,claimed_by,capability_prefix,generation,lease_expires_at,started_at,needs_labels,prefers_labels) VALUES (?,'run','claimed','trigger',1,1,'owner','owner-cap',7,?,1,?,?)`, store.DefaultTeam, until, []byte(`[]`), []byte(`[]`)); err != nil {
				t.Fatal(err)
			}
			srv := New(st, slog.New(slog.DiscardHandler))
			for range 2 {
				if !srv.reapLeased(ctx) {
					t.Fatal("controller could not acquire its reaper fence")
				}
			}
			tenant, err := st.ForTeam(ctx, store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			run, err := tenant.ReadRun(ctx, "run")
			if err != nil || run.State != store.RunRunning || run.FinishedAt != nil {
				t.Fatalf("owner recovery ended its run: state=%s, %v", run.State, err)
			}
			var status string
			var generation int64
			var disruptions int
			if err := st.DB().QueryRowContext(ctx, `SELECT status,generation,disruptions FROM triggers WHERE team=? AND id='run'`, store.DefaultTeam).Scan(&status, &generation, &disruptions); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantDisruptions, wantEvents := "claimed", 0, 0
			if expired {
				wantStatus, wantDisruptions, wantEvents = "pending", 1, 1
			}
			if status != wantStatus || disruptions != wantDisruptions || generation != 7 {
				t.Fatalf("owner status=%s generation=%d disruptions=%d", status, generation, disruptions)
			}
			events, err := tenant.ReadStateEvents(ctx, "run", 0, 10)
			if err != nil || len(events) != wantEvents {
				t.Fatalf("recovery events: %+v, %v", events, err)
			}
			if expired && events[0].Kind != "run_requeued" {
				t.Fatalf("owner event=%s", events[0].Kind)
			}
		})
	}
}
