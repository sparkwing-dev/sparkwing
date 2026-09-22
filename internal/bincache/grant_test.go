package bincache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestCacheGrantAuthenticatesAsTheRunner(t *testing.T) {
	var gotPath, gotAuth string
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.Method+" "+r.URL.Path, r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"grant":"swcg1.payload.sig","team":"team-a"}`))
	}))
	defer ctrl.Close()
	grant, err := RequestCacheGrant(context.Background(), ctrl.URL, "runner-token", "run/1")
	if err != nil {
		t.Fatal(err)
	}
	if grant != "swcg1.payload.sig" {
		t.Errorf("grant = %q", grant)
	}
	if gotPath != "POST /api/v1/runs/run/1/cache-grant" && gotPath != "POST /api/v1/runs/run%2F1/cache-grant" {
		t.Errorf("request = %q", gotPath)
	}
	if gotAuth != "Bearer runner-token" {
		t.Errorf("authorization = %q, want the runner's own token", gotAuth)
	}
}

func TestRequestCacheGrantReportsAControllerWithNoRoute(t *testing.T) {
	ctrl := httptest.NewServer(http.NotFoundHandler())
	defer ctrl.Close()
	if _, err := RequestCacheGrant(context.Background(), ctrl.URL, "runner-token", "run-1"); !errors.Is(err, ErrNoCacheGrant) {
		t.Fatalf("err = %v, want ErrNoCacheGrant", err)
	}
}

// Whatever the controller answers is handed to team code, so only a grant is
// accepted: a controller that answered with the operator token must not have
// it passed on.
func TestRequestCacheGrantRefusesAnythingButAGrant(t *testing.T) {
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"grant":"operator-cache-token"}`))
	}))
	defer ctrl.Close()
	grant, err := RequestCacheGrant(context.Background(), ctrl.URL, "runner-token", "run-1")
	if err == nil || grant != "" {
		t.Fatalf("grant = %q, err = %v; want a refusal", grant, err)
	}
}

func TestRequestCacheGrantDoesNotFollowARedirectWithTheRunnerToken(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
	}))
	defer target.Close()
	ctrl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer ctrl.Close()
	if _, err := RequestCacheGrant(context.Background(), ctrl.URL, "runner-token", "run-1"); err == nil {
		t.Fatal("a redirect produced a grant")
	}
	if leaked != "" {
		t.Fatalf("the redirect target received %q", leaked)
	}
}
