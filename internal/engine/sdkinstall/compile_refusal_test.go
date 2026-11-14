package sdkinstall

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestCompileRefusalNamesRepositoryPathsAndPipelineCode(t *testing.T) {
	output := "# repo-pipelines/jobs\njobs/ship.go:66:6: undefined: undefinedVar\n/abs/vendor/x.go:1:1: kept\nnote: not a path\n"
	got := compileRefusal(".sparkwing", output)
	want := "the pipeline code in .sparkwing does not compile:\n# repo-pipelines/jobs\n.sparkwing/jobs/ship.go:66:6: undefined: undefinedVar\n/abs/vendor/x.go:1:1: kept\nnote: not a path"
	if got.Code != apierr.PlanFailed || got.Message != want || got.Hint == "" {
		t.Fatalf("refusal = %+v\nwant message %q", got, want)
	}
	if root := compileRefusal("", "main.go:3:1: syntax error"); root.Message != "the pipeline code does not compile:\nmain.go:3:1: syntax error" {
		t.Fatalf("root pipeline refusal = %q", root.Message)
	}
}
