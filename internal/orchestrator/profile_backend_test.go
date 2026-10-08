package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/backends"
)

func neutralizeEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GITHUB_ACTIONS", "KUBERNETES_SERVICE_HOST", "XDG_CONFIG_HOME",
	} {
		os.Unsetenv(k)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestOpenReadBackendForProfile_S3State(t *testing.T) {
	neutralizeEnv(t)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	p := &profile.Profile{
		Name:  "team",
		State: &backends.Spec{Type: backends.TypeS3, Bucket: "team", Prefix: "state"},
	}
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: t.TempDir()}, p)
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile: %v", err)
	}
	defer closer.Close()
	if _, ok := b.(*backend.S3Backend); !ok {
		t.Fatalf("backend = %T, want *backend.S3Backend", b)
	}
}

func TestOpenReadBackendForProfile_SQLiteState(t *testing.T) {
	neutralizeEnv(t)
	dbPath := filepath.Join(t.TempDir(), "state.db")
	p := &profile.Profile{
		Name:  "local",
		State: &backends.Spec{Type: backends.TypeSQLite, Path: dbPath},
	}
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: t.TempDir()}, p)
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile: %v", err)
	}
	defer closer.Close()
	if _, ok := b.(*backend.StoreBackend); !ok {
		t.Fatalf("backend = %T, want *backend.StoreBackend", b)
	}
}

func TestOpenReadBackendForProfile_ControllerProfile(t *testing.T) {
	neutralizeEnv(t)
	p := &profile.Profile{
		Name:       "prod",
		Controller: &profile.ControllerSpec{URL: "https://api.example.dev", Token: "swu_test"},
	}
	b, closer, err := OpenReadBackendForProfile(context.Background(), Paths{Root: t.TempDir()}, p)
	if err != nil {
		t.Fatalf("OpenReadBackendForProfile(controller): %v", err)
	}
	defer closer.Close()
	if _, ok := b.(*backend.ClientBackend); !ok {
		t.Fatalf("backend = %T, want *backend.ClientBackend", b)
	}
}
