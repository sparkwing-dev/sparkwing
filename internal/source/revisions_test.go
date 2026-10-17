package source_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func TestSourceRevisionsSeparateIntentFromExecutableIdentity(t *testing.T) {
	old, next := strings.Repeat("a", 40), strings.Repeat("b", 40)
	refs := map[string]string{"HEAD": next, "origin/main": old, "release": old, "other": next}
	lookup := func(ref string) (string, error) {
		if sha := refs[ref]; sha != "" {
			return sha, nil
		}
		return "", fmt.Errorf("unknown ref")
	}
	for _, tc := range []struct {
		execution, pin, override, executionSHA, codeSHA string
		refusal                                         bool
	}{
		{"", "", "", next, next, false},
		{"HEAD", "origin/main", "", next, old, false},
		{"HEAD", "origin/main", "release", next, old, false},
		{"HEAD", "origin/main", "other", "", "", true},
		{"origin/main", "", "other", old, next, false},
		{"missing", "", "", "", "", true},
	} {
		t.Run(tc.execution+"/"+tc.pin+"/"+tc.override, func(t *testing.T) {
			git, code, err := source.ResolveRevisions(t.Context(), runspec.Git{Repository: runspec.Repository{Key: "local:fixture"}, Ref: tc.execution}, runspec.Source{RequestedRef: tc.pin}, tc.override, lookup)
			if tc.refusal {
				if err == nil {
					t.Fatal("invalid selection accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if git.SHA != tc.executionSHA || code.Ref != tc.codeSHA {
				t.Fatalf("wrong revision pair: %s/%s", git.SHA, code.Ref)
			}
			intent := tc.pin
			if intent == "" {
				intent = tc.override
			}
			if code.RequestedRef != intent {
				t.Fatal("lost source intent")
			}
			code.Location = runspec.SourceLocation{LocalCheckout: t.TempDir(), PipelineDir: ".sparkwing"}
			sealed := runspec.SealedRequest{Request: runspec.Request{Git: git, Source: code}}
			refs["HEAD"], refs["origin/main"] = old, next
			acquired, err := source.ForRequest(sealed)
			refs["HEAD"], refs["origin/main"] = next, old
			if err != nil || acquired.SHA != tc.executionSHA || acquired.PipelineSHA != tc.codeSHA {
				t.Fatalf("hosting reinterpreted intent: %+v %v", acquired, err)
			}
		})
	}
	git, code, err := source.ResolveRevisions(t.Context(), runspec.Git{}, runspec.Source{Ref: old}, "", lookup)
	if err != nil || git.SHA != next || code.Ref != next || code.RequestedRef != "" {
		t.Fatal("follow schedule retained a previous resolved pin", err)
	}
}
