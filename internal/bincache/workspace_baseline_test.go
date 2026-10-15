package bincache

import (
	"strings"
	"testing"
)

func TestWorkspaceBaselineResolve(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for name, tc := range map[string]struct {
		input WorkspaceBaseline
		want  WorkspaceBaseline
	}{
		"a remote-tracking ref and a commit": {
			input: WorkspaceBaseline{Ref: "origin/main", SHA: strings.ToUpper(sha)},
			want:  WorkspaceBaseline{Ref: "origin/main", SHA: sha},
		},
		"no ref":          {input: WorkspaceBaseline{SHA: sha}},
		"no commit":       {input: WorkspaceBaseline{Ref: "origin/main"}},
		"a local branch":  {input: WorkspaceBaseline{Ref: "main", SHA: sha}},
		"a parent escape": {input: WorkspaceBaseline{Ref: "origin/../../evil", SHA: sha}},
		"a git option":    {input: WorkspaceBaseline{Ref: "--upload-pack=evil/x", SHA: sha}},
		"a lock file":     {input: WorkspaceBaseline{Ref: "origin/main.lock", SHA: sha}},
		"a revision, not a commit": {
			input: WorkspaceBaseline{Ref: "origin/main", SHA: "HEAD~1"},
		},
		"nothing at all": {},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.input.Resolve()
			if tc.want == (WorkspaceBaseline{}) {
				if err == nil {
					t.Fatalf("Resolve(%+v) accepted invalid baseline", tc.input)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("Resolve = %+v, %v, want %+v", got, err, tc.want)
			}
		})
	}
}
