package controller_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestPermissionRefusalsExplainRequiredAuthority(t *testing.T) {
	f := newTenancyFixture(t, openSQLiteStore)
	token := f.mint(t, "/api/v1/tokens", f.ownerA, map[string]any{"grants": []string{"runs.read"}})
	for _, tc := range []struct {
		name, path string
		body       any
		want       []string
	}{
		{"cache", "/api/v1/data/depcache/lookup", map[string]string{"name": "private-cache", "key": "key"}, []string{"data.read", "repository", "editor", "owner"}},
		{"secrets", "/api/v1/secrets", map[string]string{"name": "PRIVATE_SECRET", "value": "private-value"}, []string{"team.admin", "owner"}},
		{"oidc", "/api/v1/runs/" + f.runA + "/oidc-token", map[string]string{"audience": "example.test"}, []string{"oidc.mint", "job credential"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := f.do("POST", tc.path, "Bearer "+token.Capability, tc.body)
			if code != http.StatusForbidden {
				t.Fatalf("status = %d: %s", code, body)
			}
			for _, want := range tc.want {
				if !strings.Contains(body, want) {
					t.Errorf("refusal must name %q: %s", want, body)
				}
			}
			for _, private := range []string{"private-cache", "PRIVATE_SECRET", "private-value", f.runA} {
				if strings.Contains(body, private) {
					t.Errorf("refusal exposes request selector %q: %s", private, body)
				}
			}
		})
	}
}

func TestPermissionRefusalsKeepForeignRunsIndistinguishableFromAbsentRuns(t *testing.T) {
	f := newTenancyFixture(t, openSQLiteStore)
	for _, auth := range []string{f.ownerB, f.readerA} {
		foreign := f.runA
		if auth == f.readerA {
			foreign = f.victim
		}
		foreignCode, foreignBody := f.do("POST", "/api/v1/runs/"+foreign+"/oidc-token", auth, map[string]string{"audience": "example.test"})
		absentCode, absentBody := f.do("POST", "/api/v1/runs/absent/oidc-token", auth, map[string]string{"audience": "example.test"})
		if foreignCode != absentCode || foreignBody != absentBody || foreignCode == http.StatusOK {
			t.Fatalf("foreign = %d %s, absent = %d %s", foreignCode, foreignBody, absentCode, absentBody)
		}
	}
}
