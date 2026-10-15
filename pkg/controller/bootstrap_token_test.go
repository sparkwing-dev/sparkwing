package controller_test

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type bootstrapIssuer struct {
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
	created, err := controller.EnsureBootstrapAdminToken(t.Context(), nil, raw)
	var api *apierr.Error
	if created || !errors.As(err, &api) || api.Code != apierr.PolicyMissing {
		t.Fatalf("missing transaction: %t %v", created, err)
	}
}
