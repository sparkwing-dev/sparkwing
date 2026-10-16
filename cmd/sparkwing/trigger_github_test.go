package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
)

func TestNativeTriggerUsesProductionGitHubLookup(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	t.Chdir(repo)
	t.Setenv("SPARKWING_CONFIG", repo+"/config.yaml")
	calls := 0
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		calls++
		if r.URL.String() != "https://api.github.com/repos/acme/app" {
			t.Fatalf("lookup: %s", r.URL)
		}
		return 404, `{"message":"missing"}`
	})
	_, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy-prod", profile: "prod"})
	var failure *apierr.Error
	if calls != 1 || !errors.As(err, &failure) || failure.Code != apierr.NotFound {
		t.Fatalf("production lookup: calls=%d err=%v", calls, err)
	}
}

func TestGitHubLookupSuppliesCanonicalID(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	profileHTTPFixture(t, func(r *http.Request) (int, string) { return 200, `{"id":17,"full_name":"acme/app","future":true}` })
	input := triggerPreparationInput(repo)
	input.githubID = func(ctx context.Context, remote string) (int64, error) {
		return lookupTriggerGitHubID(ctx, remote, os.Environ())
	}
	got, err := prepareTriggerSource(t.Context(), input)
	if err != nil || got.git.Repository.Key != "gh:17" {
		t.Fatalf("identity: %+v %v", got, err)
	}
}

func TestGitHubLookupNeverForwardsOrPrintsCredentials(t *testing.T) {
	if err := quick.Check(func(token string) bool {
		token = strings.Map(func(r rune) rune {
			if r < '!' || r > '~' {
				return 'x'
			}
			return r
		}, token)
		token = "private-test-credential-" + token
		calls := 0
		original := http.DefaultTransport
		http.DefaultTransport = profileTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer "+token {
				t.Fatal("credential escaped lookup")
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://foreign.example"}}, Body: http.NoBody}, nil
		})
		defer func() { http.DefaultTransport = original }()
		_, err := lookupTriggerGitHubID(t.Context(), "git@github.com:acme/app.git", []string{"GH_TOKEN=" + token})
		return calls == 1 && err != nil && !strings.Contains(err.Error(), token)
	}, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeWorkingTreeReachesProductionLookup(t *testing.T) {
	repo, _ := triggerPreparationRepo(t)
	t.Chdir(repo)
	t.Setenv("SPARKWING_CONFIG", repo+"/config.yaml")
	calls := 0
	profileHTTPFixture(t, func(r *http.Request) (int, string) {
		calls++
		return 404, `{"message":"missing"}`
	})
	_, err := prepareNativeTrigger(t.Context(), triggerFlags{pipeline: "deploy-prod", profile: "prod", workingTree: true})
	var failure *apierr.Error
	if calls != 1 || !errors.As(err, &failure) || failure.Code != apierr.NotFound {
		t.Fatalf("working-tree preparation: calls=%d error=%v", calls, err)
	}
}
