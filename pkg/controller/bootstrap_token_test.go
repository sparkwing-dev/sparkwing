package controller_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/store/teststore"
)

type bootstrapIssuer struct {
	*store.Store
	ensure func(context.Context, capability.ProvisionSpec) (bool, error)
}

func (f bootstrapIssuer) EnsureBootstrapCapability(ctx context.Context, spec capability.ProvisionSpec) (bool, error) {
	return f.ensure(ctx, spec)
}

func TestEnsureBootstrapAdminTokenForwardsOnlyGatedSeedProperty(t *testing.T) {
	property := func(secret [32]byte, created bool) bool {
		raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(secret[:])
		called := 0
		issuer := bootstrapIssuer{ensure: func(ctx context.Context, spec capability.ProvisionSpec) (bool, error) {
			called++
			if ctx != t.Context() || spec.Seed != raw || spec.Kind != capability.ProvisionBootstrap || spec.Authority != nil || spec.Team != "" || spec.Subject != (capability.Subject{Kind: capability.SubjectOperator, ID: controller.BootstrapAdminPrincipal}) || spec.Binding != (capability.Binding{Kind: capability.BindingNone}) || spec.Metered || spec.ExpiresAt != nil || spec.Data != nil || len(spec.ReservedPools) != 0 || len(spec.Repositories) != 0 || !slices.Equal(spec.Grants, []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore}) {
				return false, errors.New("invalid bootstrap request")
			}
			return created, nil
		}}
		got, err := controller.EnsureBootstrapAdminToken(t.Context(), issuer, raw)
		return err == nil && got == created && called == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 40}); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureBootstrapAdminTokenRefusals(t *testing.T) {
	calls := 0
	issuer := bootstrapIssuer{ensure: func(context.Context, capability.ProvisionSpec) (bool, error) {
		calls++
		return false, errors.New("commit refused")
	}}
	for _, raw := range []string{"not-a-token", "swu_provisionedbootstrapadmin00001", "swn_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32)), "swk_" + base64.RawURLEncoding.EncodeToString(make([]byte, 31)), "swk_private\n"} {
		if created, err := controller.EnsureBootstrapAdminToken(t.Context(), issuer, raw); err == nil || created {
			t.Fatalf("malformed seed accepted")
		}
	}
	if created, err := controller.EnsureBootstrapAdminToken(t.Context(), issuer, ""); err != nil || created || calls != 0 {
		t.Fatalf("empty/invalid seed reached issuer: %d %v", calls, err)
	}
	raw := "swk_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	if created, err := controller.EnsureBootstrapAdminToken(t.Context(), issuer, raw); err == nil || created || calls != 1 {
		t.Fatal("commit refusal suppressed")
	}
	var st *store.Store
	created, err := controller.EnsureBootstrapAdminToken(t.Context(), st, raw)
	var api *apierr.Error
	if created || !errors.As(err, &api) || api.Code != apierr.PolicyMissing {
		t.Fatalf("missing transaction: %t %v", created, err)
	}
}

func TestBootstrapStartupUsesDurableCapabilityAuthority(t *testing.T) {
	st, err := teststore.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	for i := range 2 {
		created, err := controller.EnsureBootstrapAdminToken(t.Context(), st, raw)
		if err != nil || created != (i == 0) {
			t.Fatalf("startup %d: %t %v", i, created, err)
		}
	}
	if ready, err := st.HasDeploymentAdministrator(t.Context()); err != nil || !ready {
		t.Fatalf("readiness: %t %v", ready, err)
	}
	req := httptest.NewRequest("GET", "/api/v1/operator/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+raw)
	out := httptest.NewRecorder()
	controller.New(st, nil).Handler().ServeHTTP(out, req)
	if out.Code != http.StatusOK {
		t.Fatalf("operator inventory: %d %s", out.Code, out.Body)
	}
	ref := capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: raw[:16]}
	if err := st.AsOperator().RevokeAuthority(t.Context(), store.Actor{Authority: ref}, ref); err != nil {
		t.Fatal(err)
	}
	if created, err := controller.EnsureBootstrapAdminToken(t.Context(), st, raw); err == nil || created {
		t.Fatal("startup revived a revoked seed")
	}
	if ready, err := st.HasDeploymentAdministrator(t.Context()); err != nil || ready {
		t.Fatalf("revoked readiness: %t %v", ready, err)
	}
}
