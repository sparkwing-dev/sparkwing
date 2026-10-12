package controller_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestRunFiltersTreatSQLMetacharactersAsLiteral(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
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
			for _, tn := range []*store.Tenant{f.teamA, f.teamB} {
				if err := tn.CreateRun(ctx, store.Run{
					ID: fmt.Sprintf("literal-%s-%d", tn.Team(), i), Pipeline: value,
					Status: value, GitBranch: value, DeclaredRepo: value, RepoURL: value,
					StartedAt: time.Now(),
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, key := range []string{"pipeline", "status", "git_branch", "repo", "repo_url"} {
			for i, value := range append(values, "missing') OR 1=1 --") {
				query := url.Values{key: {value}}
				code, raw := f.do(http.MethodGet, "/api/v1/runs?"+query.Encode(), f.ownerA, nil)
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
				if i < len(values) {
					want = 1
					if len(out.Runs) != 1 || out.Runs[0].ID != fmt.Sprintf("literal-%s-%d", f.teamA.Team(), i) {
						t.Fatalf("%s=%q returned %+v, want only the exact team A row", key, value, out.Runs)
					}
				} else if len(out.Runs) != 0 {
					t.Fatalf("missing %s=%q returned %+v", key, value, out.Runs)
				}
				filter, err := store.ParseRunFilterValidated(query)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := f.teamA.CountRuns(ctx, filter); err != nil || count != want {
					t.Fatalf("%s=%q count = %d, %v; want %d", key, value, count, err, want)
				}
			}
		}
	})
}

func TestSecretMutationsTreatSQLMetacharactersAsLiteral(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		name, pipeline := "KEY' OR 1=1 --", "ci' OR 1=1 --"
		keys := [][2]string{{name, pipeline}, {name, "ci"}, {"CONTROL", pipeline}, {"CONTROL", "ci"}}
		want := make(map[store.Team]map[[2]string]string)
		for _, tn := range []*store.Tenant{f.teamA, f.teamB} {
			want[tn.Team()] = make(map[[2]string]string)
			for i, key := range keys {
				value := fmt.Sprintf("%s-value-%d", tn.Team(), i)
				if err := tn.CreateOrReplaceSecret(store.Secret{Name: key[0], Pipeline: key[1], Value: value}, time.Now()); err != nil {
					t.Fatal(err)
				}
				want[tn.Team()][key] = value
			}
		}
		assertRows := func(t *testing.T) {
			t.Helper()
			for _, tn := range []*store.Tenant{f.teamA, f.teamB} {
				rows, err := tn.ListSecrets()
				if err != nil {
					t.Fatal(err)
				}
				got := make(map[[2]string]string)
				for _, row := range rows {
					value := row.Value
					if secrets.IsEncrypted(value) {
						opened, err := testCipher(t).OpenBound(string(tn.Team()), row.Name, row.Pipeline, row.Shared, row.Masked, value)
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
			code, raw := f.do(http.MethodPost, "/api/v1/secrets", f.ownerA,
				map[string]string{"name": key[0], "pipeline": key[1], "value": "replacement"})
			if code != http.StatusNoContent {
				t.Fatalf("replace %q on %q: status %d: %s", key[0], key[1], code, raw)
			}
			want[f.teamA.Team()][key] = "replacement"
			assertRows(t)
		}
		for _, key := range [][2]string{{"MISSING' OR 1=1 --", pipeline}, {name, "missing' OR 1=1 --"}} {
			path := "/api/v1/secrets/" + url.PathEscape(key[0]) + "?" + url.Values{"pipeline": {key[1]}}.Encode()
			if code, raw := f.do(http.MethodDelete, path, f.ownerA, nil); code != http.StatusNotFound {
				t.Fatalf("delete missing %q on %q: status %d: %s", key[0], key[1], code, raw)
			}
			assertRows(t)
		}
		for _, key := range keys {
			path := "/api/v1/secrets/" + url.PathEscape(key[0]) + "?" + url.Values{"pipeline": {key[1]}}.Encode()
			if code, raw := f.do(http.MethodDelete, path, f.ownerA, nil); code != http.StatusNoContent {
				t.Fatalf("delete %q on %q: status %d: %s", key[0], key[1], code, raw)
			}
			delete(want[f.teamA.Team()], key)
			assertRows(t)
		}
	})
}
