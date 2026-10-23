package controller_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestRunFiltersTreatSQLMetacharactersAsLiteral(t *testing.T) {
	sqlLiteralDialects(t, func(t *testing.T, f *cronsFixture, teamA, teamB *store.Tenant) {
		ctx := context.Background()
		values := []string{
			"ordinary",
			"quoted'value",
			"x') OR 1=1 --",
			"x' /* comment */",
			"x'; SELECT 'data'; --",
			"percent%_\\?&=+#",
		}
		for i, value := range values {
			for _, tn := range []*store.Tenant{teamA, teamB} {
				if err := teststore.SeedRun(f.store, ctx, tn.Team(), store.Run{RunSummary: runstate.RunSummary{ID: fmt.Sprintf("literal-%s-%d", tn.Team(), i), Pipeline: value, Status: "running", GitBranch: value, DeclaredRepo: value, RepoURL: value, StartedAt: time.Now()}}); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, key := range []string{"pipeline", "status", "git_branch", "repo", "repo_url"} {
			for i, value := range append(values, "missing') OR 1=1 --") {
				query := url.Values{key: {value}}
				r := httptest.NewRequest(http.MethodGet, "/api/v1/runs?"+query.Encode(), nil)
				r.Header.Set("Authorization", "Bearer "+f.reader)
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
				code, raw := w.Code, w.Body.String()
				if code != http.StatusOK {
					t.Fatalf("%s=%q: status %d: %s", key, value, code, raw)
				}
				var out struct {
					Runs []store.Run `json:"runs"`
				}
				if err := json.Unmarshal([]byte(raw), &out); err != nil {
					t.Fatal(err)
				}
				want := 0
				if i < len(values) && key != "status" {
					want = 1
					if len(out.Runs) != 1 || out.Runs[0].ID != fmt.Sprintf("literal-%s-%d", teamA.Team(), i) {
						t.Fatalf("%s=%q returned %+v, want only the exact team A row", key, value, out.Runs)
					}
				} else if len(out.Runs) != 0 {
					t.Fatalf("missing %s=%q returned %+v", key, value, out.Runs)
				}
				filter, err := store.ParseRunFilterValidated(query)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := teamA.CountRuns(ctx, filter); err != nil || count != want {
					t.Fatalf("%s=%q count = %d, %v; want %d", key, value, count, err, want)
				}
			}
		}
		var control struct {
			Runs []store.Run `json:"runs"`
		}
		f.call(http.MethodGet, "/api/v1/runs?status=running", f.reader, nil, http.StatusOK, &control)
		if len(control.Runs) != len(values) {
			t.Fatalf("valid status selected %d runs, want %d", len(control.Runs), len(values))
		}
		for _, row := range control.Runs {
			if !strings.HasPrefix(row.ID, "literal-"+string(teamA.Team())+"-") {
				t.Fatalf("valid status crossed tenant: %s", row.ID)
			}
		}
	})
}

func TestSecretMutationsTreatSQLMetacharactersAsLiteral(t *testing.T) {
	sqlLiteralDialects(t, func(t *testing.T, f *cronsFixture, teamA, teamB *store.Tenant) {
		name, pipeline := "KEY' OR 1=1 --", "ci' OR 1=1 --"
		keys := [][2]string{{name, pipeline}, {name, "ci"}, {"KEY", pipeline}, {"KEY", "ci"}, {"CONTROL", pipeline}, {"CONTROL", "ci"}}
		want := make(map[store.Team]map[[2]string]string)
		for _, tn := range []*store.Tenant{teamA, teamB} {
			want[tn.Team()] = make(map[[2]string]string)
			for i, key := range keys {
				value := fmt.Sprintf("%s-value-%d", tn.Team(), i)
				if err := teststore.SeedSecret(f.store, tn.Team(), store.Secret{Name: key[0], Pipeline: key[1], Value: value}, time.Now()); err != nil {
					t.Fatal(err)
				}
				want[tn.Team()][key] = value
			}
		}
		assertRows := func(t *testing.T) {
			t.Helper()
			for _, tn := range []*store.Tenant{teamA, teamB} {
				rows, err := tn.ListSecrets()
				if err != nil {
					t.Fatal(err)
				}
				got := make(map[[2]string]string)
				for _, row := range rows {
					value := row.Value
					if secrets.IsEncrypted(value) {
						opened, err := testCipher(t).OpenBound(secrets.PurposeSecret, string(tn.Team()), row.Name, row.Pipeline, row.Shared, row.Masked, value)
						if err != nil {
							t.Fatal(err)
						}
						value = opened
					}
					got[[2]string{row.Name, row.Pipeline}] = value
				}
				if !reflect.DeepEqual(got, want[tn.Team()]) {
					t.Fatalf("%s secrets = %#v, want %#v", tn.Team(), got, want[tn.Team()])
				}
			}
		}
		assertRows(t)
		for _, key := range keys {
			wantStatus := http.StatusNoContent
			if key[0] == name {
				wantStatus = http.StatusBadRequest
			}
			f.call(http.MethodPost, "/api/v1/secrets", f.writer,
				map[string]string{"name": key[0], "pipeline": key[1], "value": "replacement"}, wantStatus, nil)
			if wantStatus == http.StatusNoContent {
				want[teamA.Team()][key] = "replacement"
			}
			assertRows(t)
		}
		for _, key := range [][2]string{{"MISSING' OR 1=1 --", pipeline}, {name, "missing' OR 1=1 --"}} {
			path := "/api/v1/secrets/" + url.PathEscape(key[0]) + "?" + url.Values{"pipeline": {key[1]}}.Encode()
			if code := f.call(http.MethodDelete, path, f.writer, nil, http.StatusNotFound, nil); code != http.StatusNotFound {
				t.Fatalf("delete missing %q on %q: status %d", key[0], key[1], code)
			}
			assertRows(t)
		}
		for _, key := range keys {
			path := "/api/v1/secrets/" + url.PathEscape(key[0]) + "?" + url.Values{"pipeline": {key[1]}}.Encode()
			if code := f.call(http.MethodDelete, path, f.writer, nil, http.StatusNoContent, nil); code != http.StatusNoContent {
				t.Fatalf("delete %q on %q: status %d", key[0], key[1], code)
			}
			delete(want[teamA.Team()], key)
			assertRows(t)
		}
	})
}

func sqlLiteralDialects(t *testing.T, run func(*testing.T, *cronsFixture, *store.Tenant, *store.Tenant)) {
	t.Helper()
	for _, dialect := range []struct {
		name string
		open func(*testing.T, ...store.Option) *store.Store
	}{{"sqlite", openSQLiteStore}, {"postgres", openPostgresStore}} {
		t.Run(dialect.name, func(t *testing.T) {
			st := dialect.open(t, store.WithPolicies(func(st *store.Store) (store.Policies, error) {
				return controller.New(st, nil).WithSecretsCipher(testCipher(t)).StorePolicies(), nil
			}))
			if _, err := teststore.SQL(t, st).ExecContext(t.Context(), `INSERT INTO teams(name,created_at,updated_at) VALUES ('literal-a',1,1),('literal-b',1,1)`); err != nil {
				t.Fatal(err)
			}
			token, _, err := teststore.ProvisionCredential(t.Context(), st, capability.ProvisionSpec{Kind: capability.ProvisionMember, AllowTeamAdmin: true, Team: "literal-a", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "literal-owner"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.RunsRead, capability.TeamAdmin}})
			if err != nil {
				t.Fatal(err)
			}
			f := &cronsFixture{t: t, store: st, handler: controller.New(st, nil).WithSecretsCipher(testCipher(t)).Handler(), writer: token, reader: token}
			run(t, f, teststore.ForTeam(t, st, "literal-a"), teststore.ForTeam(t, st, "literal-b"))
		})
	}
}

func TestSecretMutationsInvalidNamesCannotChangeRowsProperty(t *testing.T) {
	sqlLiteralDialects(t, func(t *testing.T, f *cronsFixture, teamA, teamB *store.Tenant) {
		for _, team := range []*store.Tenant{teamA, teamB} {
			if err := teststore.SeedSecret(f.store, team.Team(), store.Secret{Name: "CONTROL", Value: "control"}, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		property := func(value []byte) bool {
			name := "KEY' OR 1=1 --" + fmt.Sprintf("%x", value)
			for _, method := range []string{http.MethodPost, http.MethodDelete} {
				path, body := "/api/v1/secrets/"+url.PathEscape(name), any(nil)
				if method == http.MethodPost {
					path, body = "/api/v1/secrets", map[string]string{"name": name, "value": "replacement"}
				}
				wantStatus := http.StatusBadRequest
				if method == http.MethodDelete {
					wantStatus = http.StatusNotFound
				}
				if f.status(method, path, f.writer, body) != wantStatus {
					return false
				}
			}
			for _, team := range []*store.Tenant{teamA, teamB} {
				rows, err := team.ListSecrets()
				if err != nil || len(rows) != 1 || rows[0].Name != "CONTROL" || rows[0].Value != "control" {
					return false
				}
			}
			return true
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunFilterTenantLiteralsProperty(t *testing.T) {
	sqlLiteralDialects(t, func(t *testing.T, f *cronsFixture, teamA, teamB *store.Tenant) {
		index := 0
		property := func(value []byte) bool {
			index++
			literal := fmt.Sprintf("value-%d-", index) + hex.EncodeToString(value) + "' OR 1=1 -- %_"
			for _, tenant := range []*store.Tenant{teamA, teamB} {
				if err := teststore.SeedRun(f.store, t.Context(), tenant.Team(), store.Run{RunSummary: runstate.RunSummary{ID: fmt.Sprintf("%s-%d", tenant.Team(), index), Pipeline: literal, Status: "running", GitBranch: literal, DeclaredRepo: literal, RepoURL: literal}}); err != nil {
					t.Fatal(err)
				}
			}
			for _, key := range []string{"pipeline", "git_branch", "repo", "repo_url"} {
				query := url.Values{key: {literal}}
				var out struct {
					Runs []store.Run `json:"runs"`
				}
				f.call(http.MethodGet, "/api/v1/runs?"+query.Encode(), f.reader, nil, http.StatusOK, &out)
				if len(out.Runs) != 1 || out.Runs[0].ID != fmt.Sprintf("%s-%d", teamA.Team(), index) {
					return false
				}
				filter, err := store.ParseRunFilterValidated(query)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := teamA.CountRuns(t.Context(), filter); err != nil || count != 1 {
					return false
				}
			}
			return true
		}
		if err := quick.Check(property, &quick.Config{MaxCount: 24}); err != nil {
			t.Fatal(err)
		}
	})
}
