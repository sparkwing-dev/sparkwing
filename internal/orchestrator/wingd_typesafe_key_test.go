package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestReadTypeSafeAPIKey_MissingSecretIsLoggedNotFatal(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "from-the-environment")
	var logged []string
	got := readTypeSafeAPIKey(filepath.Join(t.TempDir(), "state.db"), func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	if got != "" {
		t.Fatalf("key = %q, want none; the environment is no longer read", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "sparkwing secrets set --name "+TypeSafeAPIKeySecret) {
		t.Fatalf("logged %q, want one line naming the secret to set", logged)
	}
}

func TestTypeSafeAPIKeyFrom_TrimsAStoredNewline(t *testing.T) {
	src := secrets.SourceFunc(func(string) (string, bool, error) { return "  tsk_live_abc\n", true, nil })
	if got := typeSafeAPIKeyFrom(src, func(string, ...any) {}); got != "tsk_live_abc" {
		t.Fatalf("key = %q, want the value without surrounding whitespace", got)
	}
}
