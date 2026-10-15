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

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type queueReads struct {
	team            store.Team
	states          []*store.ConcurrencyState
	err, resolveErr error
	nilReader       bool
	resolves, reads int
}

func (f *queueReads) Team() store.Team { return f.team }

func (f *queueReads) ListConcurrencyStates(context.Context) ([]*store.ConcurrencyState, error) {
	f.reads++
	return f.states, f.err
}

func queueReadFixture() (*credentialBoundary, *queueReads) {
	b := intakeBoundary()
	b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
	return b, &queueReads{team: "team"}
}

func queueReadServer(t *testing.T, b *credentialBoundary, f *queueReads) http.Handler {
	t.Helper()
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).
		WithQueueReader(func(_ context.Context, team store.Team) (controller.QueueReader, error) {
			f.resolves++
			if team != "team" {
				t.Fatalf("selected tenant=%q", team)
			}
			if f.nilReader {
				return nil, f.resolveErr
			}
			return f, f.resolveErr
		}).Handler()
}

func TestQueueReadAuthenticatesBeforePersistence(t *testing.T) {
	b := intakeBoundary()
	b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
	h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).Handler()
	assertEngineError(t, snapshotRequest(h, b, "GET", "/api/v1/queue"), 500, apierr.PolicyMissing)
	b.raw = ""
	assertEngineError(t, snapshotRequest(h, b, "GET", "/api/v1/queue"), 401, apierr.Unauthenticated)
}

func TestQueueReadProjectsTenantGroups(t *testing.T) {
	b, f := queueReadFixture()
	f.states = []*store.ConcurrencyState{
		{
			Key: "deploy-prod", Capacity: 8, EffectiveCapacity: 3, UsedCost: 2,
			Holders: []store.ConcurrencyHolder{{RunID: "run-a", NodeID: "job-a"}, {RunID: "old", Superseded: true}},
			Waiters: []store.ConcurrencyWaiter{{RunID: "run-b", NodeID: "job-b", Position: 0}},
		},
		{Key: "plan", Capacity: 1, Holders: []store.ConcurrencyHolder{{RunID: "run-c"}}},
	}
	w := snapshotRequest(queueReadServer(t, b, f), b, "GET", "/api/v1/queue?team=foreign")
	var got wingwire.QueueState
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP %d %s headers=%v", w.Code, w.Body.String(), w.Header())
	}
	if len(got.Resources) != 2 || got.Resources[0].Key != "deploy-prod" || got.Resources[0].Capacity != 3 || got.Resources[0].Held != 2 || got.Resources[1].Capacity != 1 {
		t.Fatalf("resources=%+v", got.Resources)
	}
	if len(got.Holders) != 2 || got.Holders[0].RunID != "run-a/job-a" || got.Holders[1].RunID != "run-c" || got.Holders[0].Origin != wingwire.OriginController || got.Holders[0].ElapsedMS != 0 {
		t.Fatalf("holders=%+v", got.Holders)
	}
	if len(got.Waiters) != 1 || got.Waiters[0].RunID != "run-b/job-b" || got.Waiters[0].Position != 1 || got.Waiters[0].Origin != wingwire.OriginController || len(got.Waiters[0].WaitingOn) != 1 || got.Waiters[0].WaitingOn[0] != "deploy-prod" {
		t.Fatalf("waiters=%+v", got.Waiters)
	}
	if f.resolves != 1 || f.reads != 1 || b.bindingTarget.Team != "team" {
		t.Fatalf("resolves=%d reads=%d target=%+v", f.resolves, f.reads, b.bindingTarget)
	}
}

func TestQueueReadRefusesUnrelatedAuthority(t *testing.T) {
	for _, identity := range []string{"missing", "revoked", "expired", "membership removed", "missing parent", "machine", "claim", "trigger", "publisher", "deployment", "wrong audience", "missing grant", "no team", "relational refusal", "authentication failure"} {
		t.Run(identity, func(t *testing.T) {
			b, f := queueReadFixture()
			status, code := 401, apierr.Unauthenticated
			p := &b.snapshot.Authorities[0].Principal
			switch identity {
			case "missing":
				b.raw = ""
			case "revoked":
				p.Capability.RevokedAt = &b.snapshot.Now
			case "expired":
				p.Capability.ExpiresAt = &b.snapshot.Now
			case "membership removed":
				b.snapshot.Authorities[0].MembershipValid = false
			case "missing parent":
				p.Capability.Parent = &capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "missing-parent"}
			case "machine", "deployment":
				p.Capability.Subject.Kind = capability.SubjectMachine
				if identity == "deployment" {
					p.Capability.Subject.Kind = capability.SubjectOperator
				}
				status, code = 403, apierr.Forbidden
			case "claim", "trigger", "publisher":
				b = newCredentialBoundary()
				if identity == "trigger" {
					b = ownerBoundary()
				} else if identity == "publisher" {
					b = publisherBoundary()
				}
				b.snapshot.Authorities[0].Principal.Capability.Grants = []capability.Grant{capability.RunsRead}
				status, code = 403, apierr.Forbidden
			case "wrong audience":
				b = newCredentialBoundary()
				p = &b.snapshot.Authorities[0].Principal
				b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
				p.Capability.Audience, p.Capability.Digest = capability.NodeHost, sha256.Sum256([]byte(b.raw))
				p.Capability.Grants = []capability.Grant{capability.RunsRead}
				b.snapshot.Binding.ProcessAlive = true
				status, code = 403, apierr.Forbidden
			case "missing grant":
				p.Capability.Grants = nil
				status, code = 403, apierr.Forbidden
			case "no team":
				p.Capability.Team = ""
				status, code = 403, apierr.Forbidden
			case "relational refusal":
				b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "not a member"}
				status, code = 403, apierr.Forbidden
			case "authentication failure":
				b.readErr = errors.New("private authentication credential")
				status, code = 500, apierr.Internal
			}
			w := snapshotRequest(queueReadServer(t, b, f), b, "GET", "/api/v1/queue")
			assertEngineError(t, w, status, code)
			if f.resolves != 0 || f.reads != 0 || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("refused request read queue: resolves=%d reads=%d body=%s", f.resolves, f.reads, w.Body.String())
			}
		})
	}
}

func TestQueueReadErrorsAndMethods(t *testing.T) {
	for _, scenario := range []string{"foreign tenant", "nil reader", "unknown team", "no team", "resolver error", "private error", "not found", "partial error", "nil group", "head", "post", "old path"} {
		t.Run(scenario, func(t *testing.T) {
			b, f := queueReadFixture()
			method, path, status, code := "GET", "/api/v1/queue", 500, apierr.PolicyMissing
			switch scenario {
			case "foreign tenant":
				f.team = "foreign"
			case "nil reader":
				f.nilReader = true
			case "unknown team":
				f.resolveErr, status, code = store.ErrUnknownTeam, 404, apierr.NotFound
			case "no team":
				f.resolveErr, status, code = store.ErrNoTeam, 404, apierr.NotFound
			case "resolver error":
				f.resolveErr, code = errors.New("private resolver credential"), apierr.Internal
			case "private error", "partial error":
				f.err, code = errors.New("private queue credential"), apierr.Internal
				f.states = []*store.ConcurrencyState{{Key: "private-partial-group"}}
			case "not found":
				f.err, status, code = store.ErrNotFound, 404, apierr.NotFound
			case "nil group":
				f.states, code = []*store.ConcurrencyState{{Key: "private-partial-group"}, nil}, apierr.Internal
			case "head", "post":
				method, status, code = strings.ToUpper(scenario), 405, apierr.MethodNotAllowed
			case "old path":
				path, status = "/api/v1/queue/state", 404
			}
			w := snapshotRequest(queueReadServer(t, b, f), b, method, path)
			if scenario == "old path" {
				if w.Code != status || f.resolves != 0 {
					t.Fatalf("old path HTTP %d %s", w.Code, w.Body.String())
				}
				return
			}
			assertEngineError(t, w, status, code)
			if strings.Contains(w.Body.String(), "private") {
				t.Fatal("partial queue or private error escaped")
			}
			if (method != "GET" || scenario == "foreign tenant" || scenario == "nil reader" || f.resolveErr != nil) && f.reads != 0 {
				t.Fatal("invalid tenant or method read queue")
			}
		})
	}
}

func TestQueueReadUsesVerifiedTransportIdentity(t *testing.T) {
	for _, class := range []capability.CredentialClass{capability.CredentialSession, capability.CredentialPeer} {
		t.Run(string(class), func(t *testing.T) {
			b, f := queueReadFixture()
			p := &b.snapshot.Authorities[0].Principal
			p.Class = class
			if class == capability.CredentialSession {
				p.Authority.Kind = capability.AuthoritySession
			} else {
				p.Authority = capability.AuthorityRef{}
				p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
			}
			w := discoveryRequest(queueReadServer(t, b, f), b, "GET", "/api/v1/queue")
			var got wingwire.QueueState
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Resources)+len(got.Holders)+len(got.Waiters) != 0 || f.reads != 1 {
				t.Fatalf("HTTP %d %s reads=%d", w.Code, w.Body.String(), f.reads)
			}
		})
	}
}

var _ controller.QueueReader = (*queueReads)(nil)
