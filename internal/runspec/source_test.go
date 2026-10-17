package runspec

import (
	"encoding/hex"
	"strings"
	"testing"
	"testing/quick"
)

func TestExecutableSealRequiresBothResolvedCommits(t *testing.T) {
	property := func(execution, code string) bool {
		request := Request{Version: 1, Team: "team", Pipeline: "build", Mode: IntakeExecute, Dispatch: DispatchHost, Git: Git{Repository: Repository{Key: "gh:1"}, SHA: execution}, Source: Source{Ref: code}}
		_, err := SealRequest(request, IntakeFacts{RunID: "run"})
		_, executionErr := hex.DecodeString(execution)
		_, codeErr := hex.DecodeString(code)
		valid := executionErr == nil && codeErr == nil && (len(execution) == 40 || len(execution) == 64) && (len(code) == 40 || len(code) == 64)
		return (err == nil) == valid
	}
	for _, sha := range []string{"", "HEAD", "refs/heads/main", strings.Repeat("a", 39), strings.Repeat("z", 40), strings.Repeat("a", 40), strings.Repeat("B", 64)} {
		for _, code := range []string{"", "main", sha, strings.Repeat("b", 40)} {
			if !property(sha, code) {
				t.Fatalf("incorrect executable intake for %q/%q", sha, code)
			}
		}
	}
	if err := quick.Check(property, nil); err != nil {
		t.Fatal(err)
	}
}
