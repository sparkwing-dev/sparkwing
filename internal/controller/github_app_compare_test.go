package controller_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

func TestGitHubAppCompareUnavailableAdmitsUnverifiedRun(t *testing.T) {
	for _, comparison := range []string{"503", "500", "timeout", "unreachable", "identical", "diverged", "behind", "404", "403"} {
		t.Run(comparison, func(t *testing.T) {
			f := newAppFixture(t)
			owner := f.ghUser(501, "olga")
			f.connect(owner, 501, 7, acmeAdmin)
			f.app.SetCommit("acme/widgets", "main", headSHA)
			if code := f.subscribe(owner, "acme/widgets", "build", nil); code != http.StatusOK {
				t.Fatalf("subscribe: HTTP %d", code)
			}
			cfg := f.app.Config()
			cfg.HTTP = &http.Client{Transport: automationTransport(func(r *http.Request) (*http.Response, error) {
				if !strings.Contains(r.URL.Path, "/compare/") {
					return http.DefaultTransport.RoundTrip(r)
				}
				switch comparison {
				case "timeout":
					return nil, context.DeadlineExceeded
				case "unreachable":
					return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
				case "500", "503", "403":
					status := http.StatusServiceUnavailable
					if comparison == "500" {
						status = http.StatusInternalServerError
					} else if comparison == "403" {
						status = http.StatusForbidden
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
				default:
					return http.DefaultTransport.RoundTrip(r)
				}
			})}
			f.srv.WithGitHubApp(cfg)
			f.app.SetComparison("acme/widgets", headSHA, "refs/heads/main", comparison)
			code, out := f.deliver("push", pushPayload(7, 701, "acme/widgets", headSHA), "")
			if comparison == "403" {
				if code < 400 || len(f.triggers(owner.team)) != 0 {
					t.Fatalf("rejected compare: HTTP %d %v", code, out)
				}
				return
			}
			if code != http.StatusAccepted || out["status"] != "dispatched" {
				t.Fatalf("push: HTTP %d %v", code, out)
			}
			runs := f.triggers(owner.team)
			if len(runs) != 1 {
				t.Fatalf("runs: %+v", runs)
			}
			tenant := teststore.ForTeam(t, f.store, store.Team(owner.team))
			run, err := tenant.ReadRun(t.Context(), runs[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			proof := run.Request.Provenance
			unavailable := comparison == "503" || comparison == "500" || comparison == "timeout" || comparison == "unreachable"
			if !proof.Valid() || (proof.ProvenRepository() == "") != unavailable || proof.SourceCredentialVerified() != (comparison == "identical") {
				t.Fatalf("proof: %+v", proof)
			}
			if !unavailable {
				if !proof.Verified() || proof.UnverifiedReason() != "" {
					t.Fatalf("definite compare changed trust: %+v", proof)
				}
				return
			}
			if proof.Trust() != runspec.TrustAsserted || proof.DefaultBranchWork() || !strings.Contains(proof.UnverifiedReason(), "GitHub compare unavailable") {
				t.Fatalf("unavailable compare kept trust or lost its reason: %+v", proof)
			}
			if _, repo, ref, sha := proof.OIDC(); repo != 0 || ref != "" || sha != "" {
				t.Fatalf("unverified OIDC: %d %q %q", repo, ref, sha)
			}
			var detail controllerwire.RunDetail
			if code := f.call("GET", "/api/v1/runs/"+run.ID, owner.auth, nil, &detail); code != http.StatusOK {
				t.Fatalf("run status: HTTP %d", code)
			}
			found := false
			for _, notice := range detail.Notices {
				found = found || notice.Kind == engineid.NoticeUnverified && notice.Reason == proof.UnverifiedReason()
			}
			if !found {
				t.Fatalf("run status lost unverified reason: %+v", detail.Notices)
			}
			runner, _ := f.teamRunner(owner, "unverified-host")
			var award controllerwire.ClaimResponse
			if code := f.call("POST", "/api/v1/runners/claim", runner, githubClaimBody(capability.UnitPlan, run.ID, ""), &award); code != http.StatusOK || award.Claim == nil {
				t.Fatalf("unverified claim: HTTP %d %+v", code, award)
			}
			before := len(f.app.Minted())
			if credential, code := f.sourceCredential("Bearer "+award.Claim.Capability, run.ID); code != http.StatusNotFound || credential.Secret != "" || len(f.app.Minted()) != before {
				t.Fatalf("unverified source credential: HTTP %d %+v", code, credential)
			}
		})
	}
}
