package githooks_test

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/githooks"
)

func TestGlobalPathTreatsEmptySuccessfulConfigOutputAsUnset(t *testing.T) {
	git := func(string, ...string) (string, error) { return "\n", nil }
	if got := githooks.GlobalPath(git); got != "" {
		t.Fatalf("empty config output = %q, want unset", got)
	}
}
