package controller_test

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestStorageMaintenanceSealsAbandonedTaggedLogs(t *testing.T) {
	s, srv := storageRetentionServer(t, false)
	if _, err := teststore.SQL(t, s).Exec(`INSERT INTO log_streams(team,run_id,id,subject_kind,generation,unit_kind,prefix,retention_class,created_at,updated_at) VALUES (?,'old','stream','owner_unit',1,'plan','fixture','r30',1,1)`, store.DefaultTeam); err != nil {
		t.Fatal(err)
	}
	lease, err := s.AsOperator().AcquireSingletonLease(t.Context(), "maintenance", "other", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	read := func(want string) {
		t.Helper()
		var completeness, reason string
		if err := teststore.SQL(t, s).QueryRow("SELECT completeness,incomplete_reason FROM log_streams WHERE team=? AND run_id='old' AND id='stream'", store.DefaultTeam).Scan(&completeness, &reason); err != nil {
			t.Fatal(err)
		}
		if completeness != want || (want == "incomplete" && reason != "writer_lost") {
			t.Fatalf("log completeness=%s/%s, want %s", completeness, reason, want)
		}
	}
	srv.MaintainStorage(t.Context())
	read("open")
	if _, err := teststore.SQL(t, s).Exec("UPDATE leases SET until=0 WHERE name=? AND generation=?", lease.Name, lease.Generation); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		srv.MaintainStorage(t.Context())
		read("incomplete")
	}
}
