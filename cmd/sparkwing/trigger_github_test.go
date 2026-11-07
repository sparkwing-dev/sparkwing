package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestNativeTriggerUsesProductionGitHubLookup(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	repo, _ := triggerPreparationRepo(t)
	t.Chdir(repo)
	if err := writeConnectionFixture(profilesFixturePath(t), "prod", "https://controller.test", "team-token"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		calls++
		if r.URL.String() != "https://controller.test/api/v1/repositories/resolve" || r.Header.Get("Authorization") != "Bearer team-token" {
			t.Fatalf("lookup: %s", r.URL)
		}
		return 404, `{"error":{"code":"not_found","message":"repository unavailable"}}`
	})
	_, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy-prod", profile: "prod"})
	var failure *apierr.Error
	if calls != 1 || !errors.As(err, &failure) || failure.Code != apierr.NotFound {
		t.Fatalf("production lookup: calls=%d err=%v", calls, err)
	}
}

func TestPrivateSSHTriggerResolvesThroughSelectedController(t *testing.T) {
	origin, sha := triggerPreparationRepo(t)
	parent := t.TempDir()
	repo := filepath.Join(parent, "reference")
	runSnapshotGit(t, parent, "clone", "--quiet", "--depth", "1", "file://"+origin, repo)
	runSnapshotGit(t, repo, "remote", "set-url", "origin", "git@github.com:acme/app.git")
	if err := writeConnectionFixture(profilesFixturePath(t), "prod", "https://controller.test", "team-token"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	calls := 0
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		calls++
		if r.URL.String() != "https://controller.test/api/v1/repositories/resolve" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer team-token" {
			t.Fatalf("lookup: %s %s", r.Method, r.URL)
		}
		var body struct {
			RemoteURL string `json:"remote_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.RemoteURL != "git@github.com:acme/app.git" {
			t.Fatalf("remote: %q", body.RemoteURL)
		}
		return http.StatusOK, `{"key":"gh:17","display":"acme/app","future":true}`
	})
	input := triggerPreparationInput(repo)
	input.githubID = func(ctx context.Context, remote string) (int64, error) {
		return lookupTriggerGitHubID(ctx, remote, "prod", nil)
	}
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil || got.git.Repository.Key != "gh:17" || got.git.SHA != sha || calls != 1 {
		t.Fatalf("identity: %+v calls=%d err=%v", got, calls, err)
	}
}

func TestGitHubLookupNeverForwardsControllerCredentials(t *testing.T) {
	const token = "private-team-credential"
	if err := writeConnectionFixture(profilesFixturePath(t), "prod", "https://controller.test", token); err != nil {
		t.Fatal(err)
	}
	calls := 0
	original := http.DefaultTransport
	http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "controller.test" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatal("credential escaped controller")
		}
		return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://foreign.example"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	_, err := lookupTriggerGitHubID(t.Context(), "git@github.com:acme/app.git", "prod", nil)
	if calls != 1 || err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("redirect: calls=%d err=%v", calls, err)
	}
}

func TestNativeWorkingTreeReachesProductionLookup(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	repo, _ := triggerPreparationRepo(t)
	t.Chdir(repo)
	if err := writeConnectionFixture(profilesFixturePath(t), "prod", "https://controller.test", "team-token"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		calls++
		return 404, `{"error":{"code":"not_found","message":"repository unavailable"}}`
	})
	_, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy-prod", profile: "prod", workingTree: true})
	var failure *apierr.Error
	if calls != 1 || !errors.As(err, &failure) || failure.Code != apierr.NotFound {
		t.Fatalf("working-tree preparation: calls=%d error=%v", calls, err)
	}
}

func TestGitHubLookupFallbackRequiresExplicitToken(t *testing.T) {
	for _, tc := range []struct {
		name, body, token         string
		status, githubStatus      int
		wantFallback, wantSuccess bool
	}{
		{"private", `{"error":{"code":"not_found","message":"controller lookup"}}`, "GH_TOKEN=explicit-token", 404, 200, true, true},
		{"unsupported route", `{"error":{"code":"unsupported_route","message":"controller lookup"}}`, "GITHUB_TOKEN=explicit-token", 404, 200, false, false},
		{"App uncovered cache", `{"error":{"code":"forbidden","message":"controller lookup","details":{"reason":"repository_unauthorized"}}}`, "GH_TOKEN=explicit-token", 403, 200, true, true},
		{"no token", `{"error":{"code":"not_found","message":"controller lookup"}}`, "GH_TOKEN=", 404, 200, false, false},
		{"failed fallback", `{"error":{"code":"not_found","message":"controller lookup"}}`, "GH_TOKEN=explicit-token", 404, 404, true, false},
		{"revoked credential", `{"error":{"code":"unauthenticated","message":"controller lookup"}}`, "GH_TOKEN=explicit-token", 401, 200, false, false},
		{"missing grant", `{"error":{"code":"forbidden","message":"controller lookup","details":{"reason":"missing_grant"}}}`, "GH_TOKEN=explicit-token", 403, 200, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeConnectionFixture(profilesFixturePath(t), "prod", "https://controller.test", "team-token"); err != nil {
				t.Fatal(err)
			}
			calls := 0
			profileHTTPFixture(t, func(r *http.Request) (int, string) {
				if r.URL.Host == "controller.test" {
					if calls != 0 {
						t.Fatal("controller was not first")
					}
					return tc.status, tc.body
				}
				calls++
				if r.URL.String() != "https://api.github.com/repos/acme/app" || r.Header.Get("Authorization") != "Bearer explicit-token" {
					t.Fatalf("unexpected fallback: %s", r.URL)
				}
				return tc.githubStatus, `{"id":17}`
			})
			got, err := lookupTriggerGitHubID(t.Context(), "git@github.com:acme/app.git", "prod", []string{tc.token})
			if (calls == 1) != tc.wantFallback || (err == nil) != tc.wantSuccess {
				t.Fatalf("lookup: id=%d calls=%d err=%v", got, calls, err)
			}
			if tc.wantSuccess && got != 17 {
				t.Fatalf("identity: %d", got)
			}
			if !tc.wantSuccess && err.Error() != "controller lookup" {
				t.Fatalf("controller refusal lost: %v", err)
			}
		})
	}
}
