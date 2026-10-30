package runspec

import (
	"encoding/hex"
	"strings"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/envredact"
)

func TestSourceSealPreservesLocalWorkingTreesAndRemoteSnapshots(t *testing.T) {
	for _, source := range []Source{
		{WorkingTree: true, Location: SourceLocation{LocalCheckout: "/checkout", PipelineDir: ".sparkwing"}},
		{CommittedCheckout: true, BundleDigest: Optional[string]{Set: true, Value: strings.Repeat("b", 64)}, Location: SourceLocation{RemoteURL: "https://github.com/acme/repo.git", PipelineDir: ".sparkwing"}},
	} {
		source.Ref = strings.Repeat("a", 40)
		request := Request{Version: 1, Team: "team", Pipeline: "build", Mode: IntakeExecute, Dispatch: DispatchHost, Git: Git{Repository: Repository{Key: "gh:1"}, SHA: source.Ref}, Source: source}
		if _, err := SealRequest(request, IntakeFacts{RunID: "run"}); err != nil {
			t.Fatalf("valid source refused: %v", err)
		}
	}
}

func TestExecutableSealRequiresBothResolvedCommits(t *testing.T) {
	property := func(execution, code string) bool {
		request := Request{Version: 1, Team: "team", Pipeline: "build", Mode: IntakeExecute, Dispatch: DispatchHost, Git: Git{Repository: Repository{Key: "gh:1"}, SHA: execution}, Source: Source{Ref: code}}
		_, err := SealRequest(request, IntakeFacts{RunID: "run"})
		_, executionErr := hex.DecodeString(execution)
		_, codeErr := hex.DecodeString(code)
		valid := execution == strings.ToLower(execution) && code == strings.ToLower(code) && executionErr == nil && codeErr == nil && (len(execution) == 40 || len(execution) == 64) && (len(code) == 40 || len(code) == 64)
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

func TestSourceReferenceCredentialsUseOneRule(t *testing.T) {
	for _, ref := range []string{"git@example.com:repo#token=secret", "https://example.com/repo#token=secret", "https://example.com/repo?access_token=secret", "https://example.com/repo#%74oken=secret", "https://name:password@example.com/repo", "https://name@example.com/repo"} {
		request := Request{Version: 1, Team: "team", Pipeline: "build", Mode: IntakePublish, Dispatch: DispatchController, Git: Git{Repository: Repository{Key: "gh:1"}}, Source: Source{RequestedRef: ref}}
		if _, err := SealRequest(request, IntakeFacts{RunID: "run"}); err == nil {
			t.Fatalf("seal accepted credential reference %q", ref)
		}
	}
	for _, ref := range []string{"main", "ssh://git@example.com/repo", "https://example.com/repo?region=us"} {
		if err := envredact.ValidateSourceReference(ref); err != nil {
			t.Fatalf("credential-free ref %q: %v", ref, err)
		}
	}
}

func TestCommitIDCanonicalForm(t *testing.T) {
	for _, n := range []int{40, 64} {
		if !IsCommitID(strings.Repeat("a", n)) || IsCommitID(strings.Repeat("A", n)) || IsCommitID(strings.Repeat("g", n)) {
			t.Fatalf("commit format %d", n)
		}
	}
}
