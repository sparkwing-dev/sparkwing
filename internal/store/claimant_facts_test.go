package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestCloudPodOIDCKindIsRunner(t *testing.T) {
	st, ctx := openTestStore(t)
	tx, err := st.beginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	pod := capability.Capability{Subject: capability.Subject{Kind: capability.SubjectService, ID: "launcher"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.ClaimsLaunch}}
	facts, err := claimantFactsTx(ctx, tx, []authorityNode{{principal: capability.Principal{Capability: pod}}}, runspec.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Cloud || !facts.FreshHost || facts.OIDCKind != "runner" {
		t.Fatalf("Cloud pod facts: cloud=%v fresh=%v runner_kind=%q", facts.Cloud, facts.FreshHost, facts.OIDCKind)
	}
}
