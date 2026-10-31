package store

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
)

func TestProvenRepositoryConsumersRefusePersonalAwards(t *testing.T) {
	for _, kind := range []capability.HostKind{capability.HostPersonal, capability.HostHere} {
		t.Run(string(kind), func(t *testing.T) {
			tenant, auth := sourceReleaseFixture(t)
			s := tenant.s
			rewriteSchemaExec(t, s.db, `UPDATE runs SET spec_json=?,spec_hash='fixture',sealed_at=1,plan_json=?,plan_hash='fixture' WHERE team='a' AND id='run'`, []byte(`{"resolved":{"secret_names":["TOKEN"]}}`), []byte(`{}`))
			rewriteSchemaExec(t, s.db, `INSERT INTO secrets(team,name,value,principal,repo,pipeline,masked,shared,created_at,updated_at) VALUES ('a','TOKEN','pipeline-secret','owner','gh:1','ci',1,0,1,1)`)
			secretAuth := auth
			secretAuth.Target.SecretName = "TOKEN"
			route, _ := capability.LookupRoute("GET", "/api/v1/secrets/{name}")
			for _, rule := range route.Access {
				if rule.Binding == capability.BindDeclaredSecret {
					secretAuth.Access = rule
				}
			}
			if secret, err := tenant.ReadSecret(t.Context(), secretAuth); err != nil || secret.Value != "pipeline-secret" {
				t.Fatalf("shared host secret control: %v", err)
			}
			if _, err := mintTestOIDC(t.Context(), tenant, auth); err != nil {
				t.Fatalf("shared host OIDC control: %v", err)
			}
			if _, _, err := tenant.ReleaseSourceCredential(t.Context(), auth); err != nil {
				t.Fatalf("shared host source control: %v", err)
			}
			host := *auth.Principal.Capability.Host
			host.Kind = kind
			setSourceHost(t, tenant, auth, &host)
			if secret, err := tenant.ReadSecret(t.Context(), secretAuth); err == nil {
				t.Fatalf("personal award received secret %q", secret.Name)
			}
			if _, err := tenant.MintOIDCToken(t.Context(), auth, "sts.amazonaws.com", func(capability.OIDCFacts) (OIDCMint, error) {
				t.Fatal("personal award reached OIDC signer")
				return OIDCMint{}, nil
			}); err == nil {
				t.Fatal("personal award minted repository OIDC")
			}
			if credential, _, err := tenant.ReleaseSourceCredential(t.Context(), auth); err == nil {
				t.Fatalf("personal award received source credential %q", credential.ID)
			}
		})
	}
}
