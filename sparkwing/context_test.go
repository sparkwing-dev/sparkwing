package sparkwing

import "testing"

func TestPullRequestFromEnv_ReconstructsPRContext(t *testing.T) {
	env := map[string]string{
		EnvGitHubEventName: EventPullRequest,
		EnvPRNumber:        "42",
		EnvPRAction:        "opened",
		EnvPRBaseRef:       "main",
		EnvPRBaseSHA:       "base-sha",
		EnvPRHeadRef:       "feature/login",
		EnvPRHeadSHA:       "head-sha",
	}
	pr := PullRequestFromEnv(env)
	if pr == nil {
		t.Fatal("PullRequestFromEnv returned nil for a pull_request env")
	}
	if pr.Number != 42 {
		t.Errorf("Number=%d want 42", pr.Number)
	}
	if pr.Action != "opened" {
		t.Errorf("Action=%q want opened", pr.Action)
	}
	if pr.BaseRef != "main" || pr.BaseSHA != "base-sha" {
		t.Errorf("base=%q/%q want main/base-sha", pr.BaseRef, pr.BaseSHA)
	}
	if pr.HeadRef != "feature/login" || pr.HeadSHA != "head-sha" {
		t.Errorf("head=%q/%q want feature/login/head-sha", pr.HeadRef, pr.HeadSHA)
	}
}

func TestPullRequestFromEnv_NilWhenNotPullRequest(t *testing.T) {
	for _, env := range []map[string]string{
		nil,
		{},
		{"GITHUB_REPOSITORY": "acme/app"},
		{EnvGitHubEventName: "push"},
	} {
		if pr := PullRequestFromEnv(env); pr != nil {
			t.Errorf("PullRequestFromEnv(%v)=%+v want nil", env, pr)
		}
	}
}

func TestPullRequestFromEnv_MalformedNumberDegradesToZero(t *testing.T) {
	pr := PullRequestFromEnv(map[string]string{
		EnvGitHubEventName: EventPullRequest,
		EnvPRNumber:        "not-a-number",
		EnvPRBaseRef:       "main",
	})
	if pr == nil {
		t.Fatal("expected non-nil PR")
	}
	if pr.Number != 0 {
		t.Errorf("Number=%d want 0 for malformed input", pr.Number)
	}
}
