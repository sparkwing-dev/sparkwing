package controller

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestPeerIntakeNativeCommitProperty(t *testing.T) {
	st := newStoreForAuth(t)
	tenant, err := st.ForTeam(t.Context(), store.DefaultTeam)
	if err != nil {
		t.Fatal(err)
	}
	type verifiedPeer struct{}
	s := New(st, nil).WithEngineController(&EngineController{Store: st}).WithPeerIdentity(func(r *http.Request) capability.Subject {
		peer, _ := r.Context().Value(verifiedPeer{}).(capability.Subject)
		return peer
	})
	h := s.Handler()
	iteration := 0
	property := func(value []byte) bool {
		iteration++
		if _, err := st.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitRunSeconds: 3600}); err != nil {
			t.Fatal(err)
		}
		peer := capability.Subject{Kind: capability.SubjectLocal, ID: "uid:" + strconv.Itoa(iteration) + ":" + hex.EncodeToString(value)}
		body := controllerwire.CreateRunRequest{IdempotencyKey: peer.ID, Request: runspec.Request{
			Version: 1, Team: string(store.DefaultTeam), Pipeline: "local", Mode: runspec.IntakeExecute,
			Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
			Git:     runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}, SHA: "0123456789abcdef0123456789abcdef01234567"},
			Source:  runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"},
			Actor:   runspec.Actor{Kind: "account", ID: "forged"},
			Options: runspec.Options{Priority: runspec.Setting[int]{Optional: runspec.Optional[int]{Set: true, Value: 0}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "priority"}}},
		}}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		serve := func(verified bool) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(data))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Local-UID", peer.ID)
			if verified {
				r = r.WithContext(context.WithValue(r.Context(), verifiedPeer{}, peer))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			return w
		}
		if w := serve(false); w.Code != http.StatusUnauthorized {
			t.Logf("unverified HTTP %d: %s", w.Code, w.Body)
			return false
		}
		w := serve(true)
		var admitted controllerwire.CreateRunResponse
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &admitted) != nil {
			t.Logf("verified HTTP %d: %s", w.Code, w.Body)
			return false
		}
		run, err := tenant.ReadRun(t.Context(), admitted.RunID)
		if err != nil || run.Request.Request.Actor != (runspec.Actor{Kind: string(peer.Kind), ID: peer.ID}) || run.Request.AuthorityRef.ID == "" {
			t.Logf("committed run %+v: %v", run, err)
			return false
		}

		if !run.Request.Request.Options.Priority.Set || run.Request.Request.Options.Priority.Value != 0 || run.Request.Ceilings.OwnerExecutionTimeout.Value != time.Hour {
			t.Logf("unresolved request or missing ceiling: %+v", run.Request)
			return false
		}
		if _, err := st.AsOperator().SetComputeLimits(t.Context(), map[string]int64{store.ComputeLimitRunSeconds: 7200}); err != nil {
			t.Fatal(err)
		}
		replay := serve(true)
		var repeated controllerwire.CreateRunResponse
		if replay.Code != http.StatusOK || json.Unmarshal(replay.Body.Bytes(), &repeated) != nil || repeated.Request.Hash != admitted.Request.Hash || repeated.Request.Ceilings.OwnerExecutionTimeout.Value != time.Hour {
			t.Logf("sealed settings changed on replay HTTP %d: %s", replay.Code, replay.Body)
			return false
		}
		ref := run.Request.AuthorityRef
		foreign := body.Request
		foreign.Team = "foreign:" + hex.EncodeToString(value)
		if _, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: ref, Request: foreign}); err == nil {
			t.Log("local peer committed a foreign team request")
			return false
		}
		if err := tenant.RevokeAuthority(t.Context(), store.Actor{Authority: ref}, ref); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"", body.IdempotencyKey} {
			if _, err := tenant.AdmitRun(t.Context(), store.IntakeRequest{Authority: ref, Request: body.Request, IdempotencyKey: key}); err == nil {
				t.Log("revoked peer admitted or replayed through committing intake")
				return false
			}
		}
		if w := serve(true); w.Code != http.StatusUnauthorized {
			t.Logf("revoked HTTP %d: %s", w.Code, w.Body)
			return false
		}
		var count int
		if err := st.DB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM runs").Scan(&count); err != nil || count != iteration {
			t.Logf("committed runs %d, expected %d: %v", count, iteration, err)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestPeerIntakeBudgetsFollowPlacement(t *testing.T) {
	st := newStoreForAuth(t)
	server := New(st, nil).WithEngineController(&EngineController{Store: st}).WithPeerIdentity(func(*http.Request) capability.Subject {
		return capability.Subject{Kind: capability.SubjectLocal, ID: "uid:placement"}
	})
	handler := server.Handler()
	for _, tc := range []struct {
		name     string
		dispatch runspec.Dispatch
		here     bool
		local    bool
		want     time.Duration
	}{
		{"host", runspec.DispatchHost, false, true, 0},
		{"controller", "", false, false, limits.RunDeadline},
		{"here", runspec.DispatchHost, true, false, limits.RunDeadline},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := runspec.Request{Version: 1, Team: string(store.DefaultTeam), Pipeline: tc.name, Mode: runspec.IntakeExecute, Dispatch: tc.dispatch, Origin: runspec.OriginLocal, Git: runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}, SHA: "0123456789abcdef0123456789abcdef01234567"}, Source: runspec.Source{Ref: "0123456789abcdef0123456789abcdef01234567"}}
			if tc.dispatch == "" {
				request.Origin = runspec.OriginTrigger
			}
			if tc.here {
				request.Options.Runner = runspec.Setting[runspec.RunnerSelection]{Optional: runspec.Optional[runspec.RunnerSelection]{Set: true, Value: runspec.RunnerSelection{Kind: runspec.RunnerHere}}, Origin: runspec.SettingOrigin{Layer: runspec.LayerFlag, Key: "runner"}}
			}
			body, err := json.Marshal(controllerwire.CreateRunRequest{Request: request})
			if err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			incoming := httptest.NewRequest(http.MethodPost, "/api/v1/runs", bytes.NewReader(body))
			incoming.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(recorder, incoming)
			var admitted controllerwire.CreateRunResponse
			if recorder.Code != http.StatusCreated || json.Unmarshal(recorder.Body.Bytes(), &admitted) != nil {
				t.Fatalf("admit HTTP %d: %s", recorder.Code, recorder.Body)
			}
			if got := admitted.Request.Request.Options.OwnerExecutionTimeout.Value; got != tc.want {
				t.Fatalf("owner deadline=%v, want %v", got, tc.want)
			}
			timeout := int64(time.Hour / time.Millisecond)
			placement := store.RequestPlacement(admitted.Request.Request)
			if (placement == nodeproto.PlacementLocal) != tc.local {
				t.Fatalf("intake placement=%s local=%v", placement, tc.local)
			}
			resolved, err := settings.ResolvePlan(settings.PlanInputs{Request: admitted.Request, Declarations: runspec.CheckoutDeclarations{Version: runspec.CheckoutDeclarationsVersion}, Plan: nodeproto.PlanDoc{Jobs: []nodeproto.JobSpec{{ID: "job", TimeoutMS: &timeout}}}, Context: settings.Context{Local: placement == nodeproto.PlacementLocal}})
			if err != nil || resolved.Jobs["job"].Timeout.Value != time.Hour {
				t.Fatalf("admitted budget fails plan acceptance: %v", err)
			}
		})
	}
}
