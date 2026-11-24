package controller

import (
	"crypto/sha256"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestOperatorReissueTeamAdminHTTP(t *testing.T) {
	clock := time.Now().UTC()
	e, _ := engineRuntimeStore(t, teststore.WithClock(func() time.Time { return clock }))
	seed := reviewSeed(t, e.Store)
	h := New(e.Store, nil).Handler()
	var first controllerwire.OperatorTeamCreated
	if err := json.Unmarshal(engineClaimHTTP(t, h, seed, "POST", "/api/v1/operator/teams", `{"slug":"synthetic"}`, 201).Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/operator/teams/synthetic/admin-credentials"
	engineClaimHTTP(t, h, first.Capability, "POST", path, `{}`, 403)
	engineClaimHTTP(t, h, seed, "POST", path, `{"revoke_previous":true,"account_id":"foreign"}`, 400)
	engineClaimHTTP(t, h, seed, "POST", "/api/v1/operator/teams/absent-team/admin-credentials", `{}`, 404)

	w := engineClaimHTTP(t, h, seed, "POST", path, `{}`, 201)
	var kept controllerwire.OperatorTeamAdminReissued
	if err := json.Unmarshal(w.Body.Bytes(), &kept); err != nil {
		t.Fatal(err)
	}
	if kept.Team != "synthetic" || kept.AccountID != first.AccountID || kept.Prefix == first.Prefix || !slices.Equal(kept.Grants, first.Grants) || kept.Revoked == nil || len(kept.Revoked) != 0 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("reissue: %s", w.Body)
	}
	if !kept.ExpiresAt.Equal(clock.Add(30 * 24 * time.Hour)) {
		t.Fatalf("expires_at=%v", kept.ExpiresAt)
	}
	engineClaimHTTP(t, h, kept.Capability, "GET", "/api/v1/team/members", "", 200)
	engineClaimHTTP(t, h, first.Capability, "GET", "/api/v1/team/members", "", 200)

	parent, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(first.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	child, err := e.Store.AsOperator().DelegateCapability(t.Context(), capability.DelegateSpec{Parent: parent.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.RunsRead}, Binding: capability.Binding{Kind: capability.BindingNone}, ExpiresAt: parent.Capability.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	engineClaimHTTP(t, h, child.Raw, "GET", "/api/v1/runs", "", 200)
	w = engineClaimHTTP(t, h, seed, "POST", path, `{"revoke_previous":true}`, 201)
	var renewed controllerwire.OperatorTeamAdminReissued
	if err := json.Unmarshal(w.Body.Bytes(), &renewed); err != nil {
		t.Fatal(err)
	}
	if want := []string{first.Prefix, kept.Prefix, child.Capability.Prefix}; !slices.Equal(slices.Sorted(slices.Values(renewed.Revoked)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("revoked=%v want %v", renewed.Revoked, want)
	}
	for _, ended := range []string{first.Capability, kept.Capability, child.Raw} {
		engineClaimHTTP(t, h, ended, "GET", "/api/v1/team/members", "", 401)
	}
	engineClaimHTTP(t, h, renewed.Capability, "GET", "/api/v1/team/members", "", 200)

	engineSQL(t, e.Store, `INSERT INTO accounts(id,email,name,active_team,created_at,updated_at) VALUES ('person','person@example.test','Person','synthetic',1,1)`)
	engineSQL(t, e.Store, `INSERT INTO memberships(team,account_id,role,created_at) VALUES ('synthetic','person','owner',1)`)
	engineClaimHTTP(t, h, seed, "POST", path, `{"revoke_previous":true}`, 409)
	engineClaimHTTP(t, h, renewed.Capability, "GET", "/api/v1/team/members", "", 200)
}
