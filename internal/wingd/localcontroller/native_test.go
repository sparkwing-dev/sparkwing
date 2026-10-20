package localcontroller

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/nodehost"

	"github.com/sparkwing-dev/sparkwing/internal/store"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestNativeServicesRefusesUnknownAuthorityBeforeOpeningResources(t *testing.T) {
	var binding Binding
	c := openFixture(t, func(_ context.Context, b Binding) (Services, error) {
		binding = b
		return serviceFixture(b), nil
	})
	home := filepath.Join(t.TempDir(), "unopened")
	principal := capability.Principal{
		Class: capability.CredentialBearer, Authority: capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "forged"},
		Capability: capability.Capability{Team: string(binding.Tenant.Team()), Binding: capability.Binding{RunID: "unavailable"}},
	}
	refreshed := false
	services, err := nodehost.NewNativeServices(t.Context(), nodehost.NativeConfig{Local: func(store.NodeRow) bool { return true }, Home: home, Store: binding.Tenant, Parent: c.store.AsOperator(), Bindings: c.store.AsOperator(), Renew: c.store.AsOperator().RenewUnit, Principal: principal, Refresh: func(ctx context.Context) (capability.Principal, error) {
		refreshed = true
		route, _ := capability.LookupRoute("POST", "/api/v1/runs/{run}/heartbeat")
		_, err := capability.NewAuthenticator(c.store.AsOperator()).Authenticate(ctx, capability.Presentation{Class: capability.CredentialBearer, Raw: capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(make([]byte, 32))}, route)
		return capability.Principal{}, err
	}})
	if err == nil || services.Config.Store != nil || services.Close != nil {
		t.Fatalf("unknown authority produced native services: %+v, %v", services, err)
	}
	if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unauthenticated setup created private resources: %v", err)
	}
	if !refreshed {
		t.Fatal("native construction did not reauthorize its award")
	}
}
