package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var _ BootstrapAdminIssuer = (*store.Store)(nil)

func TestBootstrapSeedStoreOnceProperty(t *testing.T) {
	e, _ := engineRuntimeStore(t)
	random := rand.New(rand.NewPCG(984, 61))
	var initial string
	for i := range 32 {
		var secret [32]byte
		for j := range secret {
			secret[j] = byte(random.UintN(256))
		}
		raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
		created, err := EnsureBootstrapAdminToken(t.Context(), e.Store, raw)
		if err != nil || created != (i == 0) {
			t.Fatalf("bootstrap %d: created=%v err=%v", i, created, err)
		}
		if i != 0 {
			if _, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw))); err == nil {
				t.Fatal("second configured seed became authority")
			}
			continue
		}
		initial = raw
		p, err := e.Store.LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
		if err != nil || p.Capability.Subject.ID != BootstrapAdminPrincipal || p.Capability.Parent != nil {
			t.Fatalf("seed not committed through issuance: %+v, %v", p, err)
		}
		if ready, err := e.Store.HasDeploymentAdministrator(t.Context()); err != nil || !ready {
			t.Fatalf("administrator readiness: %v, %v", ready, err)
		}
		if err := e.Store.AsOperator().RevokeAuthority(t.Context(), store.Actor{Authority: p.Authority}, p.Authority); err != nil {
			t.Fatal(err)
		}
	}
	if created, err := EnsureBootstrapAdminToken(t.Context(), e.Store, initial); err != nil || created {
		t.Fatalf("revoked seed revived: %v, %v", created, err)
	}
	if ready, err := e.Store.HasDeploymentAdministrator(t.Context()); err != nil || ready {
		t.Fatalf("revoked administrator remains ready: %v, %v", ready, err)
	}
}
