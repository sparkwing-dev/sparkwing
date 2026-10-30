package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCloudSourceRestoresTemporaryDirectoryPermissions(t *testing.T) {
	scratch := t.TempDir()
	_, err := productionRuntime("dev").Bootstrap.Source(t.Context(), BootstrapOptions{SourceDir: filepath.Join(scratch, "source")})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode(); got.Perm() != 0o777 || got&os.ModeSticky == 0 {
		t.Fatalf("Cloud scratch mode=%v, want sticky and writable by package-manager users", got)
	}
}
