package controller_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type argumentRotationColumn struct {
	query, name, scope string
	raw                []byte
}

func TestOperatorRotationMigratesScheduleAndJobArguments(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*testing.T, ...store.Option) *store.Store
	}{{"sqlite", openSQLiteStore}, {"postgres", openPostgresStore}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newKeyRotationFixture(t, tc.open)
			team := f.teams[0]
			run := "run-" + string(team)
			args := []byte(`{"token":"rotation-schedule-canary"}`)
			job := []byte(`{"id":"job","arguments":[{"name":"token","type":"string","default":"rotation-job-canary","secret":true}]}`)
			plan := append(append([]byte(`{ "jobs": [`), job...), []byte(`], "unknown":9007199254740993 }`)...)
			base := append(append([]byte(`{"args":`), args...), '}')
			consent := append(append([]byte(`{"version":1,"request":{"requested_args":`), args...), []byte(`}}`)...)
			db := teststore.SQL(t, f.st)
			if _, err := db.ExecContext(t.Context(), `INSERT INTO cron_schedules(team,id,repo_path,repo_key,pipeline,cron,tz,overlap,catch_up_ns,armed_at,updated_at,cursor_at,args,override_args,override_base,consent_json,override_set_at) VALUES ($1,'schedule','https://git.example/repo','git:git.example/repo','build','* * * * *','UTC','skip',0,1,1,1,$2,$2,$3,$4,1)`, team, string(args), string(base), string(consent)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO cron_fires(team,id,schedule_id,due_at,decided_at,outcome,args) VALUES ($1,'fire','schedule',1,1,'missed',$2)`, team, string(args)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `UPDATE runs SET spec_json=$1,spec_hash='fixture',plan_json=$2,plan_hash='fixture',sealed_at=1 WHERE team=$3 AND id=$4`, []byte(`{}`), plan, team, run); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(t.Context(), `INSERT INTO nodes(team,run_id,node_id,status,spec_json,execution_json,expansion_body) VALUES ($1,$2,'job','pending',$3,$4,$5)`, team, run, job, []byte(`{}`), plan); err != nil {
				t.Fatal(err)
			}
			columns := []argumentRotationColumn{
				{`SELECT args FROM cron_schedules WHERE team=$1 AND id='schedule'`, "schedule", "schedule:args", args},
				{`SELECT override_args FROM cron_schedules WHERE team=$1 AND id='schedule'`, "schedule", "schedule:override_args", args},
				{`SELECT override_base FROM cron_schedules WHERE team=$1 AND id='schedule'`, "schedule", "schedule:override_base", base},
				{`SELECT consent_json FROM cron_schedules WHERE team=$1 AND id='schedule'`, "schedule", "schedule:consent_json", consent},
				{`SELECT args FROM cron_fires WHERE team=$1 AND id='fire'`, "fire", "schedule-fire:schedule", args},
				{`SELECT plan_json FROM runs WHERE team=$1 AND id='` + run + `'`, run, "plan", plan},
				{`SELECT spec_json FROM nodes WHERE team=$1 AND run_id='` + run + `'`, run, "node:job", job},
				{`SELECT expansion_body FROM nodes WHERE team=$1 AND run_id='` + run + `'`, run, "expansion-body:job", plan},
			}
			h := f.server(t, f.current)
			if code, result := f.rotate(t, h, f.operator, "verify=1"); code != http.StatusOK || result["plaintext_run_arguments"] != float64(10) {
				t.Fatalf("plaintext verification: %d %v", code, result)
			}
			for _, column := range columns {
				var raw []byte
				if err := db.QueryRowContext(t.Context(), column.query, team).Scan(&raw); err != nil || !bytes.Equal(raw, column.raw) {
					t.Fatalf("verify changed plaintext: %v", err)
				}
			}
			row, err := forTeam(t, f.st, team).GetCronSchedule(t.Context(), "schedule")
			if err != nil || row.Args["token"] != "rotation-schedule-canary" || row.Override == nil || row.Override.Args["token"] != "rotation-schedule-canary" {
				t.Fatalf("legacy schedule not readable: %v", err)
			}
			if code, result := f.rotate(t, h, f.operator); code != http.StatusOK || result["plaintext_run_arguments"] != float64(10) {
				t.Fatalf("migration: %d %v", code, result)
			}
			for _, column := range columns {
				var raw []byte
				if err := db.QueryRowContext(t.Context(), column.query, team).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var sealed struct {
					Envelope string `json:"sealed_arguments"`
				}
				if json.Unmarshal(raw, &sealed) != nil || bytes.Contains(raw, []byte("canary")) {
					t.Fatal("migration left a plaintext argument")
				}
				plain, err := f.next.OpenBound(secrets.PurposeRunArguments, string(team), column.name, column.scope, false, true, sealed.Envelope)
				var values struct {
					Strings []string `json:"strings"`
				}
				if err != nil || json.Unmarshal([]byte(plain), &values) != nil || len(values.Strings) != 1 || values.Strings[0] != string(column.raw) {
					t.Fatalf("migrated bytes unavailable under current key: %v", err)
				}
			}
			if code, result := f.rotate(t, f.server(t, f.next), f.operator, "verify=1"); code != http.StatusOK || result["plaintext_run_arguments"] != float64(0) || result["previous_key_only"] != float64(0) || result["run_arguments"] != float64(10) {
				t.Fatalf("current-key verification: %d %v", code, result)
			}
			if code, result := f.rotate(t, f.server(t, f.next), f.operator); code != http.StatusOK || result["plaintext_run_arguments"] != float64(0) || result["run_arguments"] != float64(10) {
				t.Fatalf("repeat rotation: %d %v", code, result)
			}
			rotated := keyRotationCipher(t, 3, 2)
			if code, result := f.rotate(t, f.server(t, rotated), f.operator, "verify=1"); code != http.StatusOK || result["previous_key_only"] != float64(19) || result["run_arguments"] != float64(10) {
				t.Fatalf("previous-key argument verification: %d %v", code, result)
			}
			if code, result := f.rotate(t, f.server(t, rotated), f.operator); code != http.StatusOK || result["previous_key_only"] != float64(19) || result["run_arguments"] != float64(10) {
				t.Fatalf("argument key rotation: %d %v", code, result)
			}
			if code, result := f.rotate(t, f.server(t, keyRotationCipher(t, 3)), f.operator, "verify=1"); code != http.StatusOK || result["previous_key_only"] != float64(0) || result["plaintext_run_arguments"] != float64(0) || result["run_arguments"] != float64(10) {
				t.Fatalf("retired-key verification: %d %v", code, result)
			}
		})
	}
}
