package controller

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func reviewPodBootstrap(t *testing.T) (*Server, string) {
	t.Helper()
	e, _ := engineRuntimeStore(t)
	seed := reviewSeed(t, e.Store)
	s := New(e.Store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	w := engineClaimHTTP(t, s.Handler(), seed, "POST", "/api/v1/operator/tokens", `{"name":"launcher","grants":["claims.launch","operator.tokens"]}`, 201)
	var launcher credentialResponse
	if err := json.Unmarshal(w.Body.Bytes(), &launcher); err != nil {
		t.Fatal(err)
	}
	engineSQL(t, e.Store, `UPDATE runs SET dispatch='controller'`)
	engineSQL(t, e.Store, `UPDATE triggers SET unit_kind='plan'`)
	issued, err := e.Store.ClaimUnit(t.Context(), store.UnitDemand{CapabilityPrefix: launcher.Prefix, Target: &store.UnitRef{Kind: capability.UnitPlan, RunID: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := e.Store.LookupCapability(t.Context(), sha256.Sum256(issued.Bearer))
	if err != nil {
		t.Fatal(err)
	}
	if principal.Capability.Delivery != capability.DeliveryBootstrap {
		t.Fatalf("delivery=%s", principal.Capability.Delivery)
	}
	return s, string(issued.Bearer)
}

func TestReviewNativeBootstrapScopeProperty(t *testing.T) {
	s, bootstrap := reviewPodBootstrap(t)
	paths := []string{"/api/v1/runs/run", "/api/v1/runs/run/receipt", "/api/v1/runs/run/attempts", "/api/v1/runs/run/steps", "/api/v1/runs/run/events", "/api/v1/runs/run/events/stream", "/api/v1/runs/run/approvals", "/api/v1/runs/run/debug-pauses"}
	random := rand.New(rand.NewPCG(93, 421))
	for range 100 {
		path := paths[random.IntN(len(paths))] + fmt.Sprintf("?team=foreign&grant=source.read&generation=%d", random.Uint64())
		engineClaimHTTP(t, s.Handler(), bootstrap, "GET", path, "", 403)
	}
	source := engineClaimHTTP(t, s.Handler(), bootstrap, "POST", "/api/v1/runs/run/source-credential", "unexpected-body", 400)
	if !strings.Contains(source.Body.String(), "source release takes no body") {
		t.Fatalf("source handler was not reached: %s", source.Body)
	}
	engineClaimHTTP(t, s.Handler(), bootstrap, "POST", "/api/v1/claims/exchange", `{}`, 200)
	engineClaimHTTP(t, s.Handler(), bootstrap, "POST", "/api/v1/claims/exchange", `{}`, 401)
}

func TestReviewBootstrapCannotUseOrdinaryAlternativeProperty(t *testing.T) {
	s, bootstrap := reviewPodBootstrap(t)
	ordinary := []capability.CredentialRule{}
	seen := map[capability.CredentialRule]bool{}
	for _, route := range capability.Routes {
		for _, rule := range route.Access {
			if rule.Credential != capability.CredentialBootstrap && !seen[rule.Credential] {
				seen[rule.Credential] = true
				ordinary = append(ordinary, rule.Credential)
			}
		}
	}
	for _, credential := range ordinary {
		route, _ := capability.LookupRoute("GET", "/api/v1/runs/{run}")
		route.Access = []capability.AccessRule{{Credential: credential, Grants: []capability.Grant{capability.SourceRead}, Binding: capability.BindRun, Liveness: capability.LiveSensitive}}
		auth, err := capability.NewAuthenticator(s.store).Authenticate(t.Context(), capability.Presentation{Class: capability.CredentialBearer, Raw: bootstrap}, route)
		if err != nil {
			t.Fatalf("native bootstrap authentication for %s: %v", credential, err)
		}
		if _, err = capability.Authorize(t.Context(), s.store, route, auth, capability.Target{Team: "a", RunID: "run"}); err == nil {
			t.Fatalf("bootstrap used ordinary alternative %s", credential)
		}
	}
}
