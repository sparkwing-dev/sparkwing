package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func cacheGrantServer(t *testing.T, cacheToken string) *Server {
	t.Helper()
	t.Setenv("SPARKWING_CACHE_TOKEN", "")
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil).WithCacheCredentials("http://cache.invalid", cacheToken)
}

func mintFor(t *testing.T, s *Server, teamOf func(*http.Request) (store.Team, error)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/run-1/cache-grant", nil)
	req.SetPathValue("id", "run-1")
	rec := httptest.NewRecorder()
	s.handleRunCacheGrant(teamOf).ServeHTTP(rec, req)
	return rec
}

func TestCacheGrantNamesTheTeamTheResolverAnswers(t *testing.T) {
	s := cacheGrantServer(t, "operator-token")
	rec := mintFor(t, s, func(*http.Request) (store.Team, error) { return "team-a", nil })
	if rec.Code != http.StatusOK {
		t.Fatalf("mint = %d: %s", rec.Code, rec.Body.String())
	}
	var body CacheGrantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	g, err := authwire.VerifyCacheGrant("operator-token", body.Grant, time.Now())
	if err != nil || g.Team != "team-a" || g.Run != "run-1" {
		t.Fatalf("minted grant = %+v, %v", g, err)
	}
}

func TestCacheGrantRefusesACallerWithNoTeam(t *testing.T) {
	s := cacheGrantServer(t, "operator-token")
	rec := mintFor(t, s, func(*http.Request) (store.Team, error) { return "", errors.New("no team") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mint with no team = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

func TestCacheGrantNeedsACacheTokenToSignWith(t *testing.T) {
	s := cacheGrantServer(t, "")
	rec := mintFor(t, s, func(*http.Request) (store.Team, error) { return "team-a", nil })
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mint with no cache token = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
