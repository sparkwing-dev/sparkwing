package credentials_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
)

func TestReadTrimsAFileAndReadsAMissingOneAsOff(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cache-token"), []byte("swc_secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	creds, err := credentials.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := creds.Read("cache-token"); err != nil || got != "swc_secret" {
		t.Fatalf("Read(cache-token) = %q, %v; want the trimmed value", got, err)
	}
	if got, err := creds.Read("cache-grant-key"); err != nil || got != "" {
		t.Fatalf("Read(cache-grant-key) = %q, %v; want an absent file to read as off", got, err)
	}
}

func TestNoDirectoryHoldsNoCredentials(t *testing.T) {
	creds, err := credentials.Open("")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := creds.Read("cache-token"); err != nil || got != "" {
		t.Fatalf("Read = %q, %v; want no credential", got, err)
	}
}

func TestOpenRefusesAPathThatIsNoDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing"), file} {
		if _, err := credentials.Open(path); err == nil {
			t.Errorf("Open(%s) = nil, want an error rather than every credential reading as off", path)
		}
	}
}
