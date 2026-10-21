package acceptance_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type creditReads struct {
	team                    store.Team
	state                   store.CreditState
	grants                  []store.CreditGrant
	charges                 []store.CreditCharge
	err                     error
	chargeErr               error
	resolveErr              error
	nilReader               bool
	resolves, reads         int
	grantLimit, chargeLimit int
}

func (f *creditReads) Team() store.Team { return f.team }

func (f *creditReads) CreditState(_ context.Context, window time.Duration) (store.CreditState, error) {
	f.reads++
	if window != 24*time.Hour {
		return store.CreditState{}, errors.New("unexpected burn window")
	}
	return f.state, f.err
}

func (f *creditReads) ListCreditGrants(_ context.Context, limit int) ([]store.CreditGrant, error) {
	f.reads++
	f.grantLimit = limit
	return f.grants, f.err
}

func (f *creditReads) ListCreditCharges(_ context.Context, limit int) ([]store.CreditCharge, error) {
	f.reads++
	f.chargeLimit = limit
	return f.charges, errors.Join(f.err, f.chargeErr)
}

func licensedCreditServer(t *testing.T, b *credentialBoundary) *controller.Server {
	t.Helper()
	return controller.NewDeployment(nil, deploymenttest.Metered(t), slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithAuthorization(consoleAuthorization(b))
}

func creditReadServer(t *testing.T, b *credentialBoundary, f *creditReads) http.Handler {
	t.Helper()
	return bindTenantPort[controller.CreditReader](licensedCreditServer(t, b),
		func(_ context.Context, team store.Team) (controller.CreditReader, error) {
			f.resolves++
			if team != "team" {
				t.Fatalf("selected tenant = %q", team)
			}
			if f.nilReader {
				return nil, f.resolveErr
			}
			return f, f.resolveErr
		}).Handler()
}

func creditReadFixture() (*credentialBoundary, *creditReads) {
	b := intakeBoundary()
	b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
	return b, &creditReads{team: "team", state: store.CreditState{BalanceMicro: 42, GrantedMicro: 100, ChargedMicro: 58}}
}

func TestCreditReadsHTTP(t *testing.T) {
	b, f := creditReadFixture()
	f.grants = []store.CreditGrant{{ID: "grant", Kind: "paid", AmountMicro: 100, CreatedAt: b.snapshot.Now}}
	f.charges = []store.CreditCharge{{ID: "charge", RunID: "run", NodeID: "job", Kind: "job", Seconds: 60, AmountMicro: 58, ChargedAt: b.snapshot.Now}}
	h := creditReadServer(t, b, f)
	w := snapshotRequest(h, b, "GET", "/api/v1/credits?team=foreign")
	var state map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &state) != nil || state["balance_micro"] != float64(42) || state["burn_window_seconds"] != float64(86400) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("balance HTTP %d %s headers=%v", w.Code, w.Body.String(), w.Header())
	}
	w = snapshotRequest(h, b, "GET", "/api/v1/credits/history?limit=7&team=foreign")
	var history struct {
		Grants []struct {
			ID string `json:"id"`
		} `json:"grants"`
		Charges []struct {
			ID string `json:"id"`
		} `json:"charges"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &history) != nil || len(history.Grants) != 1 || history.Grants[0].ID != "grant" || len(history.Charges) != 1 || history.Charges[0].ID != "charge" || f.grantLimit != 7 || f.chargeLimit != 7 || f.reads != 3 || f.resolves != 2 || b.bindingTarget.Team != "team" {
		t.Fatalf("history HTTP %d %s reads=%d resolves=%d limits=%d/%d target=%+v", w.Code, w.Body.String(), f.reads, f.resolves, f.grantLimit, f.chargeLimit, b.bindingTarget)
	}
}

func TestCreditReadsRefuseUnrelatedAuthority(t *testing.T) {
	for _, path := range []string{"/api/v1/credits", "/api/v1/credits/history"} {
		for _, identity := range []string{"missing", "revoked", "expired", "membership removed", "wrong audience", "machine", "work", "trigger", "bootstrap", "publisher", "missing grant", "no team", "relational refusal", "store read failure"} {
			t.Run(path+"/"+identity, func(t *testing.T) {
				b, f := creditReadFixture()
				code, status := apierr.Unauthenticated, 401
				p := &b.snapshot.Authorities[0].Principal
				switch identity {
				case "missing":
					b.raw = ""
				case "revoked":
					p.Capability.RevokedAt = &b.snapshot.Now
				case "expired":
					p.Capability.ExpiresAt = &b.snapshot.Now
				case "membership removed":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "wrong audience":
					b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
					p.Capability.Digest = sha256.Sum256([]byte(b.raw))
					p.Capability.Audience = capability.NodeHost
					p.Capability.Binding = capability.Binding{Kind: capability.BindingNode, RunID: "run", NodeID: "job", Holder: "runner", Generation: 4, Attempt: 2}
					b.snapshot.Binding.Current, b.snapshot.Binding.ProcessAlive = p.Capability.Binding, true
					code, status = apierr.Forbidden, 403
				case "machine":
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectMachine, ID: "machine"}
					code, status = apierr.Forbidden, 403
				case "work", "trigger", "bootstrap":
					b = newCredentialBoundary()
					if identity == "trigger" {
						b = ownerBoundary()
					}
					p = &b.snapshot.Authorities[0].Principal
					p.Capability.Grants = []capability.Grant{capability.RunsRead}
					if identity == "bootstrap" {
						p.Capability.Delivery = capability.DeliveryBootstrap
					}
					code, status = apierr.Forbidden, 403
				case "publisher":
					b = publisherBoundary()
					b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
					code, status = apierr.Forbidden, 403
				case "missing grant":
					p.Capability.Grants = nil
					code, status = apierr.Forbidden, 403
				case "no team":
					p.Capability.Team = ""
					code, status = apierr.Forbidden, 403
				case "relational refusal":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "not a member"}
					code, status = apierr.Forbidden, 403
				case "store read failure":
					b.readErr = errors.New("private database credential")
					code, status = apierr.Internal, 500
				}
				w := snapshotRequest(creditReadServer(t, b, f), b, "GET", path)
				assertEngineError(t, w, status, code)
				if f.resolves != 0 || f.reads != 0 || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("refused read reached ledger: resolves=%d reads=%d body=%s", f.resolves, f.reads, w.Body.String())
				}
			})
		}
	}
}

func TestCreditHistoryLimitsAndEmptyArrays(t *testing.T) {
	for _, query := range []string{"", "?limit=0", "?limit=1", "?limit=1000", "?limit=-1", "?limit=no", "?limit=99999999999999999999999", "?limit=1&limit=2"} {
		t.Run(query, func(t *testing.T) {
			b, f := creditReadFixture()
			w := snapshotRequest(creditReadServer(t, b, f), b, "GET", "/api/v1/credits/history"+query)
			if query == "" || query == "?limit=0" || query == "?limit=1" || query == "?limit=1000" {
				if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"grants":[],"charges":[]}` {
					t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
				}
			} else {
				assertEngineError(t, w, 400, apierr.BadRequest)
				if f.resolves != 1 || f.reads != 0 {
					t.Fatal("invalid selector reached ledger")
				}
			}
		})
	}
}

func TestCreditReadsMaskFailuresAndRejectMethods(t *testing.T) {
	for _, path := range []string{"/api/v1/credits", "/api/v1/credits/history"} {
		for _, failure := range []string{"foreign tenant", "private error", "not found", "nil reader", "unregistered team", "resolver error", "head", "post"} {
			t.Run(path+"/"+failure, func(t *testing.T) {
				b, f := creditReadFixture()
				method, status, code := "GET", 500, apierr.Internal
				switch failure {
				case "foreign tenant":
					f.team = "other"
					code = apierr.PolicyMissing
				case "private error":
					f.err = errors.New("private ledger credential")
				case "not found":
					f.err = store.ErrNotFound
					status, code = 404, apierr.NotFound
				case "nil reader":
					f.nilReader, code = true, apierr.PolicyMissing
				case "unregistered team":
					f.resolveErr, status, code = store.ErrUnknownTeam, 404, apierr.NotFound
				case "resolver error":
					f.resolveErr = errors.New("private resolver credential")
				case "head":
					method, status, code = "HEAD", 405, apierr.MethodNotAllowed
				case "post":
					method, status, code = "POST", 405, apierr.MethodNotAllowed
				}
				w := snapshotRequest(creditReadServer(t, b, f), b, method, path)
				assertEngineError(t, w, status, code)
				if strings.Contains(w.Body.String(), "private") {
					t.Fatal("private error leaked")
				}
				if failure == "foreign tenant" || failure == "nil reader" || f.resolveErr != nil || failure == "head" || failure == "post" {
					if f.reads != 0 {
						t.Fatal("invalid tenant or method reached ledger")
					}
				}
			})
		}
	}
}

func TestCreditReadsUseVerifiedTransportIdentity(t *testing.T) {
	for _, class := range []capability.CredentialClass{capability.CredentialSession, capability.CredentialPeer} {
		for _, path := range []string{"/api/v1/credits", "/api/v1/credits/history"} {
			t.Run(string(class)+path, func(t *testing.T) {
				b, f := creditReadFixture()
				p := &b.snapshot.Authorities[0].Principal
				p.Class = class
				if class == capability.CredentialSession {
					p.Authority = sessionReference(*p)
				} else {
					p.Authority = capability.AuthorityRef{}
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
				}
				w := discoveryRequest(creditReadServer(t, b, f), b, "GET", path)
				if w.Code != 200 || f.resolves != 1 {
					t.Fatalf("HTTP %d %s resolves=%d", w.Code, w.Body.String(), f.resolves)
				}
			})
		}
	}
}

func TestCreditReadsCheckAvailabilityAfterAuthentication(t *testing.T) {
	for _, path := range []string{"/api/v1/credits", "/api/v1/credits/history?limit=bad"} {
		for _, identity := range []string{"missing", "machine", "member"} {
			t.Run(path+identity, func(t *testing.T) {
				b, _ := creditReadFixture()
				status, code := 401, apierr.Unauthenticated
				switch identity {
				case "missing":
					b.raw = ""
				case "machine":
					b.snapshot.Authorities[0].Principal.Capability.Subject.Kind = capability.SubjectMachine
					status, code = 403, apierr.Forbidden
				case "member":
					status, code = 404, apierr.NotFound
				}
				h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).Handler()
				assertEngineError(t, snapshotRequest(h, b, "GET", path), status, code)
			})
		}
	}
}

func TestCreditHistoryDoesNotReturnPartialLedger(t *testing.T) {
	b, f := creditReadFixture()
	f.grants = []store.CreditGrant{{ID: "private-partial-grant"}}
	f.chargeErr = errors.New("private charge failure")
	w := snapshotRequest(creditReadServer(t, b, f), b, "GET", "/api/v1/credits/history")
	assertEngineError(t, w, 500, apierr.Internal)
	if strings.Contains(w.Body.String(), "private") || f.reads != 2 {
		t.Fatalf("reads=%d body=%s", f.reads, w.Body.String())
	}
}

func TestCreditReadsMissingDefaultReader(t *testing.T) {
	b, _ := creditReadFixture()
	h := licensedCreditServer(t, b).Handler()
	for _, path := range []string{"/api/v1/credits", "/api/v1/credits/history"} {
		assertEngineError(t, snapshotRequest(h, b, "GET", path), 500, apierr.PolicyMissing)
	}
}

var _ controller.CreditReader = (*creditReads)(nil)
