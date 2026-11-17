package store_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/internal/storetest"
)

func TestSchema41UpgradesLegacyArgumentRowsInEachDialect(t *testing.T) {
	if store.ExpectedSchemaVersion() != 41 {
		t.Fatal("argument document format requires schema 41")
	}
	baseline, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(strings.ReplaceAll(string(baseline), "\r\n", "\n")))
	if hex.EncodeToString(digest[:]) != "1bcc048252e13edb35021084dd24ad1d7f7adf9eb9cd9ee5fbc291a0ef7df189" {
		t.Fatal("fixture must use the schema-40 SQL layout")
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
			var completed int
			if err := s.DB().QueryRowContext(t.Context(), `SELECT completed FROM audit_privacy_migrations WHERE version=1`).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB().ExecContext(t.Context(), `UPDATE sparkwing_schema_version SET version=40,baseline_digest='1bcc048252e13edb35021084dd24ad1d7f7adf9eb9cd9ee5fbc291a0ef7df189'`); err != nil {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded := target.Open(t, store.WithPolicies(sealStorePolicies))
			var upgradedCompleted int
			if err := upgraded.DB().QueryRowContext(t.Context(), `SELECT completed FROM audit_privacy_migrations WHERE version=1`).Scan(&upgradedCompleted); err != nil {
				t.Fatal(err)
			}
			if upgradedCompleted != completed {
				t.Fatal("argument format upgrade changed the audit migration marker")
			}
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
			if err := upgraded.DB().QueryRowContext(t.Context(), `SELECT version FROM sparkwing_schema_version`).Scan(&version); err != nil || version != 41 {
				t.Fatalf("schema fence = %d, %v", version, err)
			}
		})
	}
}
