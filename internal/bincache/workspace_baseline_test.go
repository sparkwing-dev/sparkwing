package bincache

import (
	"strings"
	"testing"
)

func TestWorkspaceBaselineFromEnv_KeepsOnlyAUsablePair(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for name, tc := range map[string]struct {
		env  map[string]string
		want WorkspaceBaseline
	}{
		"a remote-tracking ref and a commit": {
			env:  map[string]string{WorkspaceBaseRefEnvKey: "origin/main", WorkspaceBaseSHAEnvKey: strings.ToUpper(sha)},
			want: WorkspaceBaseline{Ref: "origin/main", SHA: sha},
		},
		"no ref":          {env: map[string]string{WorkspaceBaseSHAEnvKey: sha}},
		"no commit":       {env: map[string]string{WorkspaceBaseRefEnvKey: "origin/main"}},
		"a local branch":  {env: map[string]string{WorkspaceBaseRefEnvKey: "main", WorkspaceBaseSHAEnvKey: sha}},
		"a parent escape": {env: map[string]string{WorkspaceBaseRefEnvKey: "origin/../../evil", WorkspaceBaseSHAEnvKey: sha}},
		"a git option":    {env: map[string]string{WorkspaceBaseRefEnvKey: "--upload-pack=evil/x", WorkspaceBaseSHAEnvKey: sha}},
		"a lock file":     {env: map[string]string{WorkspaceBaseRefEnvKey: "origin/main.lock", WorkspaceBaseSHAEnvKey: sha}},
		"a revision, not a commit": {
			env: map[string]string{WorkspaceBaseRefEnvKey: "origin/main", WorkspaceBaseSHAEnvKey: "HEAD~1"},
		},
		"nothing at all": {env: nil},
	} {
		t.Run(name, func(t *testing.T) {
			if got := WorkspaceBaselineFromEnv(tc.env); got != tc.want {
				t.Fatalf("WorkspaceBaselineFromEnv = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestWorkspaceBaselineEnv_RoundTripsThroughATrigger(t *testing.T) {
	baseline := WorkspaceBaseline{Ref: "origin/release-2.x", SHA: strings.Repeat("b", 40)}
	env := baseline.Env()
	if env[WorkspaceBaseRefEnvKey] != baseline.Ref || env[WorkspaceBaseSHAEnvKey] != baseline.SHA {
		t.Fatalf("Env = %v, want the baseline %+v", env, baseline)
	}
	if got := WorkspaceBaselineFromEnv(env); got != baseline {
		t.Fatalf("round trip = %+v, want %+v", got, baseline)
	}
	if env := (WorkspaceBaseline{Ref: "origin/main"}).Env(); env != nil {
		t.Fatalf("Env of an incomplete baseline = %v, want nil", env)
	}
}
