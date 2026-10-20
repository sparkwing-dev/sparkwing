package controller_test

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestTeamBoundary_AClaimWhoseCredentialNamesNoTeamIsRefused(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		raw := strings.TrimPrefix(f.runnerB, "Bearer ")
		principal, err := f.st.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
		if err != nil {
			t.Fatal(err)
		}
		query := `UPDATE capabilities SET team=NULL WHERE prefix=?`
		if f.st.Dialect() == store.DialectPostgres {
			query = `UPDATE capabilities SET team=NULL WHERE prefix=$1`
		}
		if _, err := teststore.SQL(t, f.st).ExecContext(t.Context(), query, principal.Capability.Prefix); err == nil {
			t.Fatal("schema accepted a teamless machine root")
		}
		principal.Capability.Team = ""
		principal.Capability.Subject.Kind = capability.SubjectService
		encoded, err := json.Marshal(store.CapabilityRecord{Version: 1, Value: principal.Capability})
		if err != nil {
			t.Fatal(err)
		}
		query = `UPDATE capabilities SET team=NULL,subject_kind='service',authority_json=? WHERE prefix=?`
		if f.st.Dialect() == store.DialectPostgres {
			query = `UPDATE capabilities SET team=NULL,subject_kind='service',authority_json=$1 WHERE prefix=$2`
		}
		changed, err := teststore.SQL(t, f.st).ExecContext(t.Context(), query, encoded, principal.Capability.Prefix)
		if err != nil {
			t.Fatal(err)
		}
		if count, err := changed.RowsAffected(); err != nil || count != 1 {
			t.Fatalf("removed %d credential teams: %v", count, err)
		}
		property := func(forged string, named, node bool) bool {
			kind := capability.UnitTrigger
			if node {
				kind = capability.UnitNode
			}
			body := controllerwire.ClaimRequest{RunnerVersion: "v1", Kinds: []capability.UnitKind{kind}, AvailableCores: 1}
			if named {
				body.RunID = f.runA
				if node {
					body.JobID = "n1"
				}
			}
			code, bodyText := f.do("POST", "/api/v1/runners/claim?team="+url.QueryEscape(forged), f.runnerB, body)
			var refusal apierr.Envelope
			if code != http.StatusForbidden || json.Unmarshal([]byte(bodyText), &refusal) != nil || refusal.Error.Code != apierr.Forbidden || refusal.Error.Details["reason"] != string(apierr.ReasonNoTeam) || strings.Contains(bodyText, `"capability"`) {
				t.Fatalf("teamless claim = %d: %s", code, bodyText)
			}
			var awards int
			if err := teststore.SQL(t, f.st).QueryRowContext(t.Context(), `SELECT COUNT(*) FROM capabilities WHERE issuance='award'`).Scan(&awards); err != nil {
				t.Fatal(err)
			}
			return awards == 0
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTeamBoundaryRunPrivacyProperty(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		serial := 0
		property := func(value uint64) bool {
			serial++
			id := "private-" + strconv.FormatUint(value, 16) + "-" + strconv.Itoa(serial)
			pipeline := "pipeline-" + id
			private := "argument-" + id
			if err := teststore.SeedRun(f.st, t.Context(), f.teamA.Team(), store.Run{ID: id, Pipeline: pipeline, Status: "running", Args: map[string]string{"private": private}}); err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/runs/" + id
			if code, body := f.do("GET", path, f.ownerA, nil); code != 200 || !strings.Contains(body, pipeline) || strings.Contains(body, private) {
				t.Fatalf("owner read = %d: %s", code, body)
			}
			for _, auth := range []string{f.ownerB, f.everyScopeB} {
				for _, route := range []struct {
					method, suffix string
					body           any
				}{
					{"GET", "", nil},
					{"GET", "?include=secret_values", nil},
					{"GET", "/receipt", nil},
					{"GET", "/attempts", nil},
					{"DELETE", "", nil},
					{"POST", "/cancel", nil},
					{"POST", "/retry", controllerwire.RetryRequest{Mode: "all", IdempotencyKey: id}},
				} {
					code, body := f.do(route.method, path+route.suffix, auth, route.body)
					if code != http.StatusNotFound || strings.Contains(body, pipeline) || strings.Contains(body, private) || strings.Contains(body, "route not found") {
						t.Fatalf("foreign %s %s = %d: %s", route.method, route.suffix, code, body)
					}
				}
				if code, body := f.do("GET", "/api/v1/runs", auth, nil); code != 200 || strings.Contains(body, id) || strings.Contains(body, private) {
					t.Fatalf("foreign list = %d: %s", code, body)
				}
			}
			row, err := f.teamA.ReadRun(t.Context(), id)
			if err != nil || row.State != store.RunRunning || row.CancelRequestedAt != nil || row.Request.Request.RequestedArgs["private"] != private {
				t.Fatalf("foreign mutation: %+v: %v", row, err)
			}
			own, err := f.teamB.ListRuns(t.Context(), store.RunFilter{Limit: 100})
			return err == nil && len(own) == 0
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTeamBoundaryOutputHidesRunExistenceProperty(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		serial := 0
		property := func(value uint64) bool {
			serial++
			id := "output-private-" + strconv.FormatUint(value, 16) + "-" + strconv.Itoa(serial)
			if err := teststore.SeedRun(f.st, t.Context(), f.teamA.Team(), store.Run{ID: id, Pipeline: "private-pipeline-" + id, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			for _, auth := range []string{f.ownerB, f.everyScopeB} {
				code, body := f.do("GET", "/api/v1/runs/"+id+"/jobs/n1/output", auth, nil)
				missingCode, missingBody := f.do("GET", "/api/v1/runs/missing-run/jobs/n1/output", auth, nil)
				if code != http.StatusNotFound || missingCode != code || missingBody != body {
					t.Logf("output existence oracle: foreign=%d %s missing=%d %s", code, body, missingCode, missingBody)
					return false
				}
			}
			return true
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
			t.Fatal(err)
		}
	})
}
