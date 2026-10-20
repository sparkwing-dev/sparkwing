package store

import (
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestAuthenticationDigestSelectsOnlyLiveAuthorityProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(1901, 803))
	for i := range 40 {
		s := authorityStore(t)
		now := time.Unix(1700000000, int64(random.IntN(1000000000))).UTC()
		s.clock = func() time.Time { return now }
		member := authorityMember(t, s, "member", RoleOwner)
		expires := now.Add(time.Duration(1+random.IntN(3600)) * time.Second)
		issued, err := s.AsOperator().ProvisionCapability(t.Context(), capability.ProvisionSpec{
			Kind: capability.ProvisionMember, Authority: &member.Authority, Team: "a",
			Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "member"},
			Binding: capability.Binding{Kind: capability.BindingNone},
			Grants:  []capability.Grant{capability.RunsRead}, ExpiresAt: &expires,
		})
		if err != nil {
			t.Fatal(err)
		}
		type persisted struct {
			Body    []byte
			Expires int64
			Used    *int64
		}
		var before, after persisted
		query := `SELECT authority_json,expires_at,last_used_at FROM capabilities WHERE prefix=?`
		if err := s.db.QueryRow(query, issued.Capability.Prefix).Scan(&before.Body, &before.Expires, &before.Used); err != nil {
			t.Fatal(err)
		}
		for j := range 8 {
			raw := []byte(issued.Raw)
			index := len(capability.ControllerPrefix) + random.IntN(len(raw)-len(capability.ControllerPrefix))
			raw[index] ^= byte(1 + random.IntN(255))
			digest := sha256.Sum256(raw)
			if j == 0 {
				digest = sha256.Sum256([]byte(fmt.Sprint(random.Uint64())))
			}
			lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Digest: digest, Audience: capability.Controller}
			if snapshot, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{}); err == nil || len(snapshot.Authorities) != 0 {
				t.Fatal("changed bearer resolved authority")
			}
		}
		lookup := capability.CredentialLookup{Class: capability.CredentialBearer, Digest: issued.Capability.Digest, Audience: capability.Controller}
		snapshot, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{})
		if err != nil || len(snapshot.Authorities) != 1 || snapshot.Authorities[0].Principal.Capability.Prefix != issued.Capability.Prefix {
			t.Fatalf("complete digest: %+v, %v", snapshot, err)
		}
		err = s.db.QueryRow(query, issued.Capability.Prefix).Scan(&after.Body, &after.Expires, &after.Used)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("authentication changed persisted authority: %v", err)
		}
		if i%2 == 0 {
			tenant := &Tenant{s: s, team: "a"}
			actor := Actor{Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: issued.Capability.Prefix}, CapabilityPrefix: issued.Capability.Prefix}
			if err := tenant.RevokeAuthority(t.Context(), actor, actor.Authority); err != nil {
				t.Fatal(err)
			}
		} else {
			now = expires
		}
		if snapshot, err := s.AsOperator().ReadAuthentication(t.Context(), lookup, capability.RequestEvidence{}); err == nil || len(snapshot.Authorities) != 0 {
			t.Fatal("ended credential resolved authority")
		}
	}
}
