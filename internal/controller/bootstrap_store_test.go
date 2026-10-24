package controller

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

var _ BootstrapAdminIssuer = (*store.Operator)(nil)

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
		created, err := EnsureBootstrapAdminToken(t.Context(), e.Store.AsOperator(), raw)
		if (err != nil) != (i != 0) || created != (i == 0) {
			t.Fatalf("bootstrap %d: created=%v err=%v", i, created, err)
		}
		if i != 0 {
			if _, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw))); err == nil {
				t.Fatal("second configured seed became authority")
			}
			continue
		}
		initial = raw
		p, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw)))
		if err != nil || p.Capability.Subject.ID != BootstrapAdminPrincipal || p.Capability.Parent != nil {
			t.Fatalf("seed not committed through issuance: %+v, %v", p, err)
		}
		if ready, err := e.Store.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || !ready {
			t.Fatalf("administrator readiness: %v, %v", ready, err)
		}
		if err := e.Store.AsOperator().RevokeAuthority(t.Context(), runstate.Actor{Authority: p.Authority}, p.Authority); err != nil {
			t.Fatal(err)
		}
	}
	if created, err := EnsureBootstrapAdminToken(t.Context(), e.Store.AsOperator(), initial); !errors.Is(err, store.ErrBootstrapSeedConsumed) || created {
		t.Fatalf("revoked seed: created=%v err=%v, want ErrBootstrapSeedConsumed so a restart only warns", created, err)
	}
	if ready, err := e.Store.AsOperator().HasDeploymentAdministrator(t.Context()); err != nil || ready {
		t.Fatalf("revoked administrator remains ready: %v, %v", ready, err)
	}
	t.Run("administrator without marker", func(t *testing.T) {
		e, _ := engineRuntimeStore(t)
		if created, err := EnsureBootstrapAdminToken(t.Context(), e.Store.AsOperator(), initial); err != nil || !created {
			t.Fatalf("initial administrator: created=%v err=%v", created, err)
		}
		engineSQL(t, e.Store, `DELETE FROM settings WHERE key='bootstrap_admin'`)
		raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		if created, err := EnsureBootstrapAdminToken(t.Context(), e.Store.AsOperator(), raw); err == nil || created {
			t.Fatalf("second administrator without marker: created=%v err=%v", created, err)
		}
		if _, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(raw))); err == nil {
			t.Fatal("seed became authority while an administrator already existed")
		}
	})
}
