package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type adapterTenant struct {
	RunTransactions
	dataTransactions
	sourceTransactions
}

func (*adapterTenant) Team() store.Team { return "team" }

func TestAdaptersShareRequestTenant(t *testing.T) {
	tenant := &adapterTenant{}
	resolutions := 0
	resolve := func(context.Context, store.Team) (TenantHandle, error) { resolutions++; return tenant, nil }
	ctx := context.WithValue(t.Context(), requestTenantKey{}, &requestTenant{resolve: resolve})
	engine := &EngineController{ResolveTenant: resolve}
	data := &DataController{ResolveTenant: resolve}
	source := &SourceController{ResolveTenant: resolve}
	if _, err := engine.runTransactions(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := data.dataTransactions(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.transactions(ctx, "team"); err != nil {
		t.Fatal(err)
	}
	if resolutions != 1 {
		t.Fatalf("tenant resolved %d times", resolutions)
	}
	if _, err := data.dataTransactions(ctx, "foreign"); err == nil {
		t.Fatal("request changed tenant")
	}
	if resolutions != 1 {
		t.Fatal("foreign team reached tenant resolver")
	}
}

func TestUnboundDataAuthorityRefusesMissingPolicy(t *testing.T) {
	server := New(nil, nil).WithDataObjects(nil)
	_, err := server.dataController.authorityTenant(t.Context(), blob.Authority{CapabilityRef: "missing"})
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.PolicyMissing {
		t.Fatalf("missing authority: %v", err)
	}
}
