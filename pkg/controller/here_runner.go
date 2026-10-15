package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
