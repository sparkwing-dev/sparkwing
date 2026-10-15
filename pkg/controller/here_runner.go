package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// safety: `pipeline trigger --runner here` hands its caller a credential that
// hosts one run on the caller's machine. It claims that run's pinned trigger
// once, reaches only that run's routes, and expires; its secrets reach the run
// only through the claims it takes, exactly as on any other runner.

// safety: the credential must claim its run within store.HereRunnerClaimWindow;
// once claimed, the run's trigger and node claims live as long as the
// credential, which matches the six hours a runner gives a hosted run.
const hereRunnerCredentialTTL = 6 * time.Hour

// safety: the reason lands on the run's event stream and the audit record, so it is one bounded line.
const maxHereReason = 200

const hereRunner = "here"

func (s *Server) hereRunnerRequest(r *http.Request, body triggerReq) (*store.HereRunner, error) {
	switch body.Runner {
	case "":
		if body.Reason != "" {
			return nil, errors.New(`reason is recorded only for runner "here"`)
		}
		return nil, nil
	case hereRunner:
	default:
		return nil, errors.New(`runner must be "here" or absent`)
	}
	reason := strings.TrimSpace(body.Reason)
	switch {
	case reason == "":
		return nil, errors.New(`runner "here" needs a reason, which the run records`)
	case len(reason) > maxHereReason:
		return nil, errors.New("reason is at most 200 bytes")
	case strings.ContainsFunc(reason, unicode.IsControl):
		return nil, errors.New("reason is one line of text")
	case body.ParentRunID != "" || body.RetryOf != "":
		return nil, errors.New(`runner "here" starts a new run; it takes no parent_run_id or retry_of`)
	}
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p == nil {
		return nil, errors.New(`runner "here" needs an authenticated caller to record`)
	}
	return &store.HereRunner{
		Reason: reason, By: p.Name, CreatedBy: p.AccountID,
		Scopes: store.MachineTokenScopes(), TTL: hereRunnerCredentialTTL,
	}, nil
}

func (s *Server) admitHereTrigger(ctx context.Context, t *store.Tenant, in triggerIntake, here store.HereRunner) (string, *store.Token, error) {
	trig, run := triggerRows(in)
	raw, tok, err := t.CreateHereTriggerWithRun(ctx, trig, run, here, in.At)
	if err != nil {
		if errors.Is(err, store.ErrComputeLimit) {
			return "", nil, err
		}
		return "", nil, errors.Join(errors.New("persist the here-runner trigger and its run"), err)
	}
	s.recordQueueActivity(in.At)
	return raw, tok, nil
}

// safety: the reason is the caller's own justification, recorded so the audit
// line answers why as well as who; it was held to one bounded line above.
func noteHereRunnerAudit(ctx context.Context, tok *store.Token, reason string) {
	noteAuditTarget(ctx, "runner", hereRunner)
	noteAuditTarget(ctx, "runner_token_prefix", tok.Prefix)
	noteAuditTarget(ctx, "reason", reason)
}

// safety: routes are allowlisted, so a route added later answers 403 to a
// here-runner credential until someone decides how it is confined. Neither
// queue claim is listed: the credential takes only its own trigger by id.
func (s *Server) hereRunnerFence(next http.Handler) http.Handler {
	fence := s.hereFenceMux(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		if p == nil {
			next.ServeHTTP(w, r)
			return
		}
		runID, isHere := store.HereRunnerRunID(p.Name)
		if !isHere {
			next.ServeHTTP(w, r)
			return
		}
		if runID == "" {
			writeHereFenceRefusal(w, p, "this credential does not name the run it is bound to")
			return
		}
		fence.ServeHTTP(w, r)
	})
}

func (s *Server) hereFenceMux(next http.Handler) *http.ServeMux {
	pass := next
	byRun := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		runID, _ := store.HereRunnerRunID(p.Name)
		if r.PathValue("id") != runID {
			writeHereFenceRefusal(w, p, "this credential hosts run "+runID+" only")
			return
		}
		next.ServeHTTP(w, r)
	})
	byPipeline := s.hereFencePipeline(next)
	mux := http.NewServeMux()
	// safety: a run create is bound to the caller's live claim on the run's
	// trigger, and this credential can hold only its own.
	mux.Handle("POST /api/v1/runs", pass)
	mux.Handle("/api/v1/runs/{id}", byRun)
	mux.Handle("/api/v1/runs/{id}/{rest...}", byRun)
	mux.Handle("/api/v1/triggers/{id}", byRun)
	mux.Handle("/api/v1/triggers/{id}/{rest...}", byRun)
	// safety: each of these binds to a live claim on a named run.
	mux.Handle("/api/v1/pipelines/{name}/profile", byPipeline)
	mux.Handle("/api/v1/pipelines/{name}/profile/{rest...}", byPipeline)
	mux.Handle("GET /api/v1/secrets/{name}", pass)
	mux.Handle("GET /api/v1/auth/whoami", pass)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		writeHereFenceRefusal(w, p, "a here-runner credential cannot use "+r.Method+" "+r.URL.Path)
	})
	return mux
}

// safety: the profile read is gated by scope alone, so a here-runner reaches a
// pipeline's profile only while it holds the live claim on its run of that pipeline.
func (s *Server) hereFencePipeline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		runID, _ := store.HereRunnerRunID(p.Name)
		tenant, ok := s.requestTenant(w, r)
		if !ok {
			return
		}
		trig, err := tenant.GetTrigger(r.Context(), runID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeInternalError(w, r, "here runner fence", err)
			return
		}
		var holder store.ClaimIdentity
		if err == nil {
			holder, err = tenant.TriggerClaimant(r.Context(), runID)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeInternalError(w, r, "here runner fence", err)
			return
		}
		live := err == nil && trig.Status == "claimed" && trig.LeaseExpiresAt != nil && time.Now().Before(*trig.LeaseExpiresAt) &&
			holder.TokenPrefix == p.TokenPrefix
		if !live || trig.Pipeline != r.PathValue("name") {
			writeHereFenceRefusal(w, p, "this credential reads only the profile of run "+runID+"'s pipeline while it holds the run's claim")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeHereFenceRefusal(w http.ResponseWriter, p *Principal, msg string) {
	writeAuthError(w, http.StatusForbidden, authErrorBody{Code: "forbidden", Principal: p.label(), Message: msg})
}
