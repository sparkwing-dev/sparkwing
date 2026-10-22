package sdkinstall

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestGoSDKInstallCacheIgnoresDisabledWorkspace(t *testing.T) {
	installNativeToolFixture(t)
	root := t.TempDir()
	dir, dependency := filepath.Join(root, "pipeline"), filepath.Join(root, "dependency")
	for _, path := range []string{dir, dependency} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeInput(t, dir, "go.mod", []byte("module fixture\ngo 1.26\nrequire "+engine.GoModule+" v0.20.0\n"))
	writeInput(t, dependency, "go.mod", []byte("module example.com/unrelated\ngo 1.26\n"))
	workspace := filepath.Join(root, "go.work")
	writeInput(t, root, "go.work", []byte("go 1.26\nuse (\n ./pipeline\n ./dependency\n)\n"))
	t.Setenv("GOWORK", workspace)
	pin, err := engine.ReadPin(dir, engine.GoSDK)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	install := func() string {
		t.Helper()
		masker := secrets.NewMasker()
		environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: masker}, procenv.Runtime{})
		if err != nil {
			t.Fatal(err)
		}
		_, cached, err := installGoSDK(t.Context(), dir, engine.SDKInstallRequest{ExecutableDirectory: t.TempDir()}, pin, environment, &diagnostics, masker)
		if err != nil {
			t.Fatalf("install: %v: %s", err, &diagnostics)
		}
		return cached
	}
	first := install()
	property := func(body []byte) bool {
		writeInput(t, dependency, "unrelated.go", body)
		next := install()
		if first != next {
			t.Logf("disabled workspace changed cache identity: %s -> %s", first, next)
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
	writeInput(t, root, "go.work", []byte("invalid ambient workspace"))
	if next := install(); first != next {
		t.Fatalf("disabled workspace contents changed cache identity: %s -> %s", first, next)
	}
}
