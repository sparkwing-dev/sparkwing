package sparks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadManifest(t *testing.T) {
	t.Run("absent file returns (nil, nil)", func(t *testing.T) {
		dir := t.TempDir()
		m, err := LoadManifest(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m != nil {
			t.Fatalf("expected nil manifest, got %#v", m)
		}
	})

	t.Run("happy path", func(t *testing.T) {
		dir := t.TempDir()
		contents := `libraries:
  - name: sparks-core
    source: github.com/sparkwing-dev/sparks-core
    version: ^v0.10.0
  - name: sparkwing-go
    source: github.com/example/my-sparks
    version: latest
`
		if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := LoadManifest(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m == nil || len(m.Libraries) != 2 {
			t.Fatalf("expected 2 libraries, got %#v", m)
		}
		if m.Libraries[0].Name != "sparks-core" ||
			m.Libraries[0].Source != "github.com/sparkwing-dev/sparks-core" ||
			m.Libraries[0].Version != "^v0.10.0" {
			t.Fatalf("first library wrong: %#v", m.Libraries[0])
		}
		if m.Libraries[1].Version != "latest" {
			t.Fatalf("second library version wrong: %#v", m.Libraries[1])
		}
	})

	t.Run("malformed yaml", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte("libraries: [oh: no"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(dir); err == nil {
			t.Fatal("expected parse error")
		}
	})

	t.Run("entry missing source", func(t *testing.T) {
		dir := t.TempDir()
		contents := `libraries:
  - name: no-source
    version: latest
`
		if err := os.WriteFile(filepath.Join(dir, ManifestFilename), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadManifest(dir); err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("empty sparkwingDir rejected", func(t *testing.T) {
		if _, err := LoadManifest(""); err == nil {
			t.Fatal("expected error for empty dir")
		}
	})
}
