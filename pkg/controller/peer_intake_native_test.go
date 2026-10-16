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

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type peerIntakePolicy struct{}

func (peerIntakePolicy) ResolveIntake(_ context.Context, _ capability.AuthorityResolver, in store.IntakeRequest) (store.IntakePolicyFacts, error) {
	return store.IntakePolicyFacts{Options: &in.Request.Options, LogRetentionClass: "r30", LogRetentionDays: 30}, nil
}

func TestPeerIntakeNativeCommitProperty(t *testing.T) {
	st := newStoreForAuth(t)
	st.SetIntakePolicy(peerIntakePolicy{})
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
		peer := capability.Subject{Kind: capability.SubjectLocal, ID: "uid:" + strconv.Itoa(iteration) + ":" + hex.EncodeToString(value)}
		body := controllerwire.CreateRunRequest{IdempotencyKey: peer.ID, Request: runspec.Request{
			Version: 1, Team: string(store.DefaultTeam), Pipeline: "local", Mode: runspec.IntakeExecute,
			Dispatch: runspec.DispatchHost, Origin: runspec.OriginLocal,
			Git:   runspec.Git{Repository: runspec.Repository{Key: "git:example.com/acme/repo"}, SHA: "head"},
			Actor: runspec.Actor{Kind: "account", ID: "forged"},
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
		if w := serve(true); w.Code != http.StatusOK {
			t.Logf("replay HTTP %d: %s", w.Code, w.Body)
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
