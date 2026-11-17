package store_test

import (
	"encoding/json"
	"maps"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestSchema40UpgradesLegacyArgumentRowsInEachDialect(t *testing.T) {
	if store.ExpectedSchemaVersion() != 40 {
		t.Fatal("argument document format requires schema 40")
	}
	for _, dialect := range []struct {
		name   string
		target func(*testing.T) *storetest.Target
	}{{"sqlite", storetest.NewSQLite}, {"postgres", storetest.NewPostgres}} {
		t.Run(dialect.name, func(t *testing.T) {
			target := dialect.target(t)
			s := target.Open(t)
			tenant, err := s.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]string{"sealed_arguments": "legacy-token", "body": "", "argument_document_version": "1"}
			if _, _, err := tenant.ArmCronSchedule(t.Context(), store.CronSchedule{ID: "legacy", RepoPath: "/repo", Pipeline: "build", Cron: "* * * * *", TZ: "UTC", Args: args}); err != nil {
				t.Fatal(err)
			}
			legacy, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(t.Context(), storetest.Rebind(s, `UPDATE cron_schedules SET args=? WHERE team=? AND id='legacy'`), string(legacy), store.DefaultTeam); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(t.Context(), `UPDATE sparkwing_schema_version SET version=39,baseline_digest='bd376ce39b5fdee54f286dd73111f3644264b8cc4cb864a887cd589436eae19b'`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded := target.Open(t, store.WithPolicies(sealStorePolicies))
			tenant, err = upgraded.ForTeam(t.Context(), store.DefaultTeam)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := tenant.ListCronSchedules(t.Context())
			if err != nil || len(rows) != 1 || !maps.Equal(rows[0].Args, args) {
				t.Fatalf("upgrade changed legacy arguments: %v", err)
			}
			if err := upgraded.ResealScheduleArgumentsForTest(t.Context(), store.DefaultTeam); err != nil {
				t.Fatal(err)
			}
			rows, err = tenant.ListCronSchedules(t.Context())
			if err != nil || len(rows) != 1 || !maps.Equal(rows[0].Args, args) {
				t.Fatalf("reseal broke schedule listing: %v", err)
			}
			var version int
			if err := upgraded.DB().QueryRowContext(t.Context(), `SELECT version FROM sparkwing_schema_version`).Scan(&version); err != nil || version != 40 {
				t.Fatalf("schema fence = %d, %v", version, err)
			}
		})
	}
}
