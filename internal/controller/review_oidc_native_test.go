package controller

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
	"github.com/sparkwing-dev/sparkwing/internal/apierr"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

func TestNativeOIDCMintCommittedIdentityProperty(t *testing.T) {
	e, _, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	tenant, err := e.Store.ForTeam(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	run, err := tenant.ReadRun(t.Context(), "run")
	if err != nil {
		t.Fatal(err)
	}
	run.Request.Request.Origin = runspec.OriginLocal
	run.Request.Request.Git = runspec.Git{Repository: runspec.Repository{Key: "gh:42", Display: "github.com/acme/api"}, Ref: "refs/heads/main", SHA: strings.Repeat("a", 40)}
	sealed := sealWith(t, run.Request, assertedProvenance(t, "ci", "refs/heads/main", strings.Repeat("a", 40)))
	engineSQL(t, e.Store, `UPDATE runs SET request_json=?,repo_key='gh:42' WHERE team='a' AND id='run'`, sealed)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := oidcissuer.New("https://issuer.example", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	handler := New(e.Store, nil).WithEngineController(e.EngineController).WithOIDCIssuer(issuer).Handler()
	response := engineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("award: %v %s", err, response.Body)
	}
	var lease int64
	if err := teststore.SQL(t, e.Store).QueryRow(`SELECT lease_expires_at FROM nodes WHERE team='a' AND run_id='run' AND node_id='job'`).Scan(&lease); err != nil {
		t.Fatal(err)
	}
	property := func(forged [8]byte) bool {
		response := engineClaimHTTP(t, handler, award.Claim.Capability, "POST", fmt.Sprintf("/api/v1/runs/run/oidc-token?team=%x&repository=%x&runner_kind=service", forged, forged), `{"audience":"vault"}`, http.StatusOK)
		var token OIDCTokenResponse
		if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
			t.Log(err)
			return false
		}
		parts := strings.Split(token.Token, ".")
		if len(parts) != 3 {
			return false
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			return false
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
			return false
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return false
		}
		var claims map[string]any
		if json.Unmarshal(raw, &claims) != nil {
			return false
		}
		return claims["team"] == "a" && claims["pipeline"] == "ci" && claims["trigger"] == "manual" && claims["runner_kind"] == "runner" && claims["repository"] == nil && claims["sha"] == nil && claims["ref"] == nil && claims["repository_id"] == nil && !token.ExpiresAt.After(time.Unix(0, lease))
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 16}); err != nil {
		t.Fatal(err)
	}

	engineSQL(t, e.Store, `INSERT INTO github_app_deliveries(digest,delivery_id,received_at,event) VALUES ('verified-delivery','verified-delivery',1,'push')`)
	for _, tc := range []struct {
		name, event, ref, wantEvent, wantRef string
		verified                             bool
		origin                               runspec.Origin
		wantID                               int64
		// sealsApp is whether intake sealed this run's provenance from its App delivery.
		sealsApp bool
	}{
		{"manual_cannot_claim_app", "push", "refs/heads/main", "manual", "", true, runspec.OriginLocal, 0, false},
		{"unverified_app", "push", "refs/heads/main", "manual", "", false, runspec.OriginApp, 0, false},
		{"push", "push", "refs/heads/main", "push", "refs/heads/main", true, runspec.OriginApp, 42, true},
		{"tag", "push", "refs/tags/v1", "push", "refs/tags/v1", true, runspec.OriginApp, 42, true},
		{"pull_request", "pull_request", "refs/pull/7/head", "pull_request", "refs/pull/7/head", true, runspec.OriginApp, 42, true},
		{"release", "release", "refs/tags/v1", "release", "refs/tags/v1", true, runspec.OriginApp, 42, true},
		{"create", "create", "refs/heads/topic", "create", "refs/heads/topic", true, runspec.OriginApp, 42, true},
		{"delete", "delete", "refs/heads/topic", "delete", "refs/heads/topic", true, runspec.OriginApp, 42, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := run.Request
			request.Request.Origin = tc.origin
			request.Request.Git.Ref = tc.ref
			request.Request.Git.App = runspec.Optional[runspec.AppFacts]{Set: true, Value: runspec.AppFacts{DeliveryKey: "verified-delivery", Event: tc.event, RepositoryID: 42, Ref: tc.ref, SHA: strings.Repeat("a", 40)}}
			if tc.verified {
				request.Request.Git.App.Value.VerifiedAt = time.Now().UTC()
			}
			request.Request.PullRequest = runspec.Optional[runspec.PullRequest]{Set: true, Value: runspec.PullRequest{Number: 7}}
			p := assertedProvenance(t, "ci", tc.ref, strings.Repeat("a", 40))
			if tc.sealsApp {
				p = appProvenance(t, request.Request.Git.App.Value, request.Request.PullRequest, "ci")
			}
			encoded := sealWith(t, request, p)
			engineSQL(t, e.Store, `UPDATE runs SET request_json=? WHERE team='a' AND id='run'`, encoded)
			engineSQL(t, e.Store, `UPDATE github_app_deliveries SET event=? WHERE digest='verified-delivery'`, tc.event)
			response := engineClaimHTTP(t, handler, award.Claim.Capability, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`, 200)
			var token OIDCTokenResponse
			if err := json.Unmarshal(response.Body.Bytes(), &token); err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(token.Token, ".")
			payload, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			var claims map[string]any
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Fatal(err)
			}
			repositoryID := ""
			if tc.wantID > 0 {
				repositoryID = fmt.Sprint(tc.wantID)
			}
			if ref, _ := claims["ref"].(string); claims["trigger"] != tc.wantEvent || ref != tc.wantRef || !strings.Contains(claims["sub"].(string), "repository_id:"+repositoryID+":pipeline:") {
				t.Fatalf("identity: %s", payload)
			}
		})
	}
	engineSQL(t, e.Store, `DELETE FROM github_app_deliveries WHERE digest='verified-delivery'`)
	response = engineClaimHTTP(t, handler, award.Claim.Capability, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`, 200)
	var retained OIDCTokenResponse
	if err := json.Unmarshal(response.Body.Bytes(), &retained); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(retained.Token, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["trigger"] != "delete" || claims["ref"] != "refs/heads/topic" || !strings.Contains(claims["sub"].(string), "repository_id:42:pipeline:") {
		t.Fatalf("replay expiry changed sealed identity: %s", payload)
	}
	principal, err := e.Store.AsOperator().LookupCapability(t.Context(), sha256.Sum256([]byte(award.Claim.Capability)))
	if err != nil {
		t.Fatal(err)
	}
	supplied := capability.Authorized{Principal: principal, Target: capability.Target{Team: "a", RunID: "run"}}
	forged := supplied
	forged.Target.Team = "foreign"
	if _, err := tenant.ReadOIDCFacts(t.Context(), forged); err == nil {
		t.Fatal("cross-team persisted facts escaped")
	}
	engineSQL(t, e.Store, `UPDATE nodes SET cancel_requested_at=1 WHERE team='a' AND run_id='run' AND node_id='job'`)

	if _, err := tenant.ReadOIDCFacts(t.Context(), supplied); err == nil {
		t.Fatal("stale authorization minted after cancellation")
	} else {
		var failure *apierr.Error
		if !errors.As(err, &failure) || failure.Code != apierr.Unauthenticated {
			t.Fatalf("cancellation failure: %v", err)
		}
	}
	engineClaimHTTP(t, handler, award.Claim.Capability, "POST", "/api/v1/runs/run/oidc-token", `{"audience":"vault"}`, 401)
}
