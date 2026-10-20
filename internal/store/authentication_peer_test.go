package store

import (
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestPeerAuthorityPersistsAndRevocationEndsDelegation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			if err := s.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	lookup := capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}}
	first, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil {
		t.Fatal(err)
	}
	p := first.Authorities[0].Principal
	if p.Class != capability.CredentialPeer || p.Authority.ID == "" || p.Authority.ID != p.Capability.Prefix || p.Capability.Team != string(DefaultTeam) {
		t.Fatalf("peer authority: %+v", p)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
	if err != nil || second.Authorities[0].Principal.Authority != p.Authority {
		t.Fatalf("reopened peer authority: %+v, %v", second, err)
	}
	spec := capability.DelegateSpec{Parent: p.Authority, Audience: capability.Controller, Grants: []capability.Grant{capability.NodesClaim, capability.TriggersClaim}, Binding: capability.Binding{Kind: capability.BindingNone}}
	child, err := s.AsOperator().DelegateCapability(t.Context(), spec)
	if err != nil || child.Capability.Parent == nil || *child.Capability.Parent != p.Authority {
		t.Fatalf("peer delegation: %+v, %v", child, err)
	}
	route, _ := capability.LookupRoute("POST", "/api/v1/runners/claim")
	presentation := capability.Presentation{Class: capability.CredentialBearer, Raw: child.Raw}
	if _, err := capability.NewAuthenticator(s.AsOperator()).Authenticate(t.Context(), presentation, route); err != nil {
		t.Fatal(err)
	}
	tenant, err := s.ForTeam(t.Context(), DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	if err := tenant.RevokeAuthority(t.Context(), Actor{Authority: p.Authority}, p.Authority); err != nil {
		t.Fatal(err)
	}
	if _, err := capability.NewAuthenticator(s.AsOperator()).Authenticate(t.Context(), presentation, route); err == nil {
		t.Fatal("revoked peer ancestor authenticated a descendant")
	}
	if _, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{}); err == nil {
		t.Fatal("peer authentication replaced its revoked root")
	}
	if _, err := s.AsOperator().DelegateCapability(t.Context(), spec); err == nil {
		t.Fatal("revoked peer delegated new authority")
	}
	if err := s.AsOperator().CheckBinding(t.Context(), p, capability.BindNone, capability.Target{Team: string(DefaultTeam)}); err == nil {
		t.Fatal("revoked peer retained resource access")
	}
}

func TestPeerAuthorityRejectsForgedSelectorsProperty(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	property := func(value []byte, mutation uint8) bool {
		lookup := capability.CredentialLookup{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: "uid:" + hex.EncodeToString(value)}}
		evidence := capability.RequestEvidence{}
		switch mutation % 5 {
		case 0:
			lookup.Peer.Kind = capability.SubjectAccount
		case 1:
			lookup.Peer.ID = ""
		case 2:
			lookup.Digest[0] = 1
		case 3:
			lookup.Audience = capability.NodeHost
		case 4:
			evidence.Target.Team = "foreign:" + hex.EncodeToString(value)
		}
		if _, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, evidence); err == nil {
			return false
		}
		var count int
		if err := s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM capabilities").Scan(&count); err != nil || count != 0 {
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128}); err != nil {
		t.Fatal(err)
	}
}

func TestPeerIntakeAndClaimUseSharedRelations(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	request, err := json.Marshal(runspec.SealedRequest{Request: runspec.Request{Git: runspec.Git{Repository: runspec.Repository{Key: "gh:1", Display: "https://github.com/acme/repo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	rewriteSchemaExec(t, s.db, `INSERT INTO runs(team,id,number,pipeline,status,request_json,request_hash,request_sealed_at,repo_key,repo_display,dispatch,intake_mode,allowed_pools,created_principal,created_at,log_retention_class,log_retention_days) VALUES ('default','local-run',1,'ci','pending',?,'request',1,'gh:1','https://github.com/acme/repo','host','execute',?,'local',1,'r30',30)`, request, []byte(`[]`))
	rewriteSchemaExec(t, s.db, `INSERT INTO triggers(team,id,unit_kind,available_at,created_at,needs_labels,prefers_labels) VALUES ('default','local-run','trigger',1,1,?,?)`, []byte(`[]`), []byte(`[]`))
	for _, path := range []string{"/api/v1/runs", "/api/v1/runs/{run}/claim", "/api/v1/runners/claim"} {
		t.Run(path, func(t *testing.T) {
			route, ok := capability.LookupRoute("POST", path)
			if !ok {
				t.Fatal("route policy missing")
			}
			target := capability.Target{Team: string(DefaultTeam)}
			if path != "/api/v1/runs" {
				target.ClaimKinds = []capability.UnitKind{capability.UnitTrigger}
				if path != "/api/v1/runners/claim" {
					target.RunID = "local-run"
				}
			}
			presentation := capability.Presentation{Class: capability.CredentialPeer, Peer: capability.Subject{Kind: capability.SubjectLocal, ID: "local:1000"}, Evidence: capability.RequestEvidence{Target: target}}
			auth, err := capability.NewAuthenticator(s.AsOperator()).Authenticate(t.Context(), presentation, route)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := capability.Authorize(t.Context(), s.AsOperator(), route, auth, target); err != nil {
				t.Fatal("verified local operation refused:", err)
			}
			property := func(forged string) bool {
				foreign := target
				foreign.Team = "foreign:" + forged
				_, err := capability.Authorize(t.Context(), s.AsOperator(), route, auth, foreign)
				return err != nil
			}
			if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
