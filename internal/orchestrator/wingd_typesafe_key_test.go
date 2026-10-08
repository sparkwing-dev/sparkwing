package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
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
