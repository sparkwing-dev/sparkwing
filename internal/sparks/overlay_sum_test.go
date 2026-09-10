package sparks

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

type fixtureModule struct {
	path    string
	version string
	goMod   string
	files   map[string]string
}

func supersedingFixture() []fixtureModule {
	leaf := func(v string) fixtureModule {
		return fixtureModule{
			path:    "example.com/leaf",
			version: v,
			goMod:   "module example.com/leaf\n\ngo 1.16\n",
			files:   map[string]string{"leaf.go": "package leaf\n\nfunc V() string { return \"" + v + "\" }\n"},
		}
	}
	product := func(v, leafVer string) fixtureModule {
		return fixtureModule{
			path:    "example.com/product",
			version: v,
			goMod: "module example.com/product\n\ngo 1.26.0\n\nrequire example.com/lib v1.0.0\n\n" +
				"require example.com/leaf " + leafVer + " // indirect\n",
			files: map[string]string{"product.go": "package product\n\nimport \"example.com/lib\"\n\nfunc P() string { return lib.L() }\n"},
		}
	}
	return []fixtureModule{
		leaf("v1.0.0"),
		leaf("v1.2.0"),
		{
			path:    "example.com/lib",
			version: "v1.0.0",
			goMod:   "module example.com/lib\n\ngo 1.16\n\nrequire example.com/leaf v1.0.0\n",
			files:   map[string]string{"lib.go": "package lib\n\nimport \"example.com/leaf\"\n\nfunc L() string { return leaf.V() }\n"},
		},
		product("v0.1.0", "v1.0.0"),
		product("v0.2.0", "v1.2.0"),
	}
}

const supersedingConsumerGoMod = `module consumer.example

go 1.26.0

require example.com/product v0.1.0

require (
	example.com/leaf v1.0.0 // indirect
	example.com/lib v1.0.0 // indirect
)
`

func buildMultiFileProxy(t *testing.T, mods []fixtureModule) string {
	t.Helper()
	proxyRoot := t.TempDir()
	for _, m := range mods {
		src := t.TempDir()
		writeFile(t, filepath.Join(src, "go.mod"), m.goMod)
		for name, content := range m.files {
			writeFile(t, filepath.Join(src, name), content)
		}
		escaped, err := module.EscapePath(m.path)
		if err != nil {
			t.Fatal(err)
		}
		vdir := filepath.Join(proxyRoot, filepath.FromSlash(escaped), "@v")
		if err := os.MkdirAll(vdir, 0o755); err != nil {
			t.Fatal(err)
		}
		list, err := os.OpenFile(filepath.Join(vdir, "list"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := list.WriteString(m.version + "\n"); err != nil {
			t.Fatal(err)
		}
		if err := list.Close(); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(vdir, m.version+".info"),
			`{"Version":"`+m.version+`","Time":"2024-01-01T00:00:00Z"}`)
		writeFile(t, filepath.Join(vdir, m.version+".mod"), m.goMod)

		var buf bytes.Buffer
		if err := modzip.CreateFromDir(&buf, module.Version{Path: m.path, Version: m.version}, src); err != nil {
			t.Fatalf("CreateFromDir %s@%s: %v", m.path, m.version, err)
		}
		if err := os.WriteFile(filepath.Join(vdir, m.version+".zip"), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return proxyRoot
}

func supersedingConsumer(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	proxyDir := buildMultiFileProxy(t, supersedingFixture())

	// safety: materializeSum returns early under a workspace, so a test that
	// leaves GOWORK alone exercises nothing.
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxyDir))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOSUMDB", "*")
	t.Setenv("GOMODCACHE", writableTempDir(t))
	t.Setenv("GOTOOLCHAIN", "local")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), supersedingConsumerGoMod)
	writeFile(t, filepath.Join(dir, "use.go"), "package consumer\n\nimport _ \"example.com/product\"\n")
	return dir
}

func buildWithOverlay(t *testing.T, dir string) error {
	t.Helper()
	cmd := exec.Command("go", "build", "-modfile="+filepath.Join(dir, OverlayModfileName), "./...")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &buildFailure{output: string(out), err: err}
	}
	return nil
}

type buildFailure struct {
	output string
	err    error
}

func (b *buildFailure) Error() string { return b.err.Error() + ": " + b.output }

// TestMaterializeSumKeepsSupersededChecksums pins the checksums an overlay needs
// for requirements minimal version selection superseded. example.com/leaf v1.0.0
// stays in the module graph because example.com/lib is unpruned, so its go.mod
// hash is required even though selection picks v1.2.0.
func TestMaterializeSumKeepsSupersededChecksums(t *testing.T) {
	dir := supersedingConsumer(t)

	if _, err := WriteOverlay(context.Background(), dir,
		map[string]string{"example.com/product": "v0.2.0"}); err != nil {
		t.Fatalf("WriteOverlay: %v", err)
	}

	sum, err := os.ReadFile(filepath.Join(dir, OverlaySumfileName))
	if err != nil {
		t.Fatalf("read sum: %v", err)
	}
	if len(sum) == 0 {
		t.Fatal("sum is empty, so materializeSum never ran")
	}
	if !strings.Contains(string(sum), "example.com/leaf v1.0.0/go.mod") {
		t.Errorf("superseded requirement's go.mod checksum missing:\n%s", sum)
	}
	if err := buildWithOverlay(t, dir); err != nil {
		t.Errorf("overlay does not build: %v", err)
	}
}

// TestWriteOverlayRepairsSumAfterFailedMaterialization proves an overlay whose
// sum never materialized is repaired on the next call rather than left for a
// manual go mod download.
func TestWriteOverlayRepairsSumAfterFailedMaterialization(t *testing.T) {
	dir := supersedingConsumer(t)
	resolved := map[string]string{"example.com/product": "v0.2.0"}

	proxy := os.Getenv("GOPROXY")
	t.Setenv("GOPROXY", "off")
	if _, err := WriteOverlay(context.Background(), dir, resolved); err == nil {
		t.Fatal("expected the first WriteOverlay to fail with the proxy off")
	}
	t.Setenv("GOPROXY", proxy)

	if _, err := WriteOverlay(context.Background(), dir, resolved); err != nil {
		t.Fatalf("second WriteOverlay: %v", err)
	}
	sum, err := os.ReadFile(filepath.Join(dir, OverlaySumfileName))
	if err != nil {
		t.Fatalf("read sum: %v", err)
	}
	if len(sum) == 0 {
		t.Fatal("sum is empty, so materializeSum never ran")
	}
	if err := buildWithOverlay(t, dir); err != nil {
		t.Errorf("overlay does not build without a manual go mod download: %v", err)
	}
}
