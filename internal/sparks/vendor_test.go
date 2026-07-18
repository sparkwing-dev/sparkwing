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

func TestCopyModuleTree_ExcludesVendorAndCopiesRest(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "go.mod"), "module example.com/m\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(src, "hello.go"), "package m\n")
	writeFile(t, filepath.Join(src, "spark.json"), `{"name":"m"}`)
	writeFile(t, filepath.Join(src, "sub", "nested.go"), "package sub\n")
	writeFile(t, filepath.Join(src, "vendor", "dep", "d.go"), "package dep\n")

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyModuleTree(src, dst); err != nil {
		t.Fatalf("copyModuleTree: %v", err)
	}

	for _, rel := range []string{"go.mod", "hello.go", "spark.json", filepath.Join("sub", "nested.go")} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("expected %s copied: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "vendor")); !os.IsNotExist(err) {
		t.Errorf("expected vendor/ excluded, stat err = %v", err)
	}
}

func TestMakeTreeWritable_AddsWriteBit(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "ro.go")
	writeFile(t, f, "package m\n")
	if err := os.Chmod(f, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := makeTreeWritable(root); err != nil {
		t.Fatalf("makeTreeWritable: %v", err)
	}
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o200 == 0 {
		t.Errorf("expected write bit set, got mode %v", info.Mode())
	}
}

func TestAddReplaceDirective_PreservesRequires(t *testing.T) {
	dir := t.TempDir()
	goMod := filepath.Join(dir, "go.mod")
	original := "module consumer\n\ngo 1.26.0\n\nrequire example.com/dep v1.2.3\n"
	writeFile(t, goMod, original)

	raw, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatal(err)
	}
	if err := addReplaceDirective(goMod, raw, "example.com/dep", "./sparks/dep"); err != nil {
		t.Fatalf("addReplaceDirective: %v", err)
	}
	got, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatal(err)
	}
	s := string(got)
	if !strings.Contains(s, "require example.com/dep v1.2.3") {
		t.Errorf("require line lost:\n%s", s)
	}
	if !strings.Contains(s, "replace example.com/dep => ./sparks/dep") {
		t.Errorf("replace directive missing:\n%s", s)
	}
}

func TestVendor_RefusesExistingDest(t *testing.T) {
	sparkwingDir := t.TempDir()
	writeFile(t, filepath.Join(sparkwingDir, "go.mod"), "module consumer\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(sparkwingDir, VendoredDirName, "templates", "keep.go"), "package templates\n")

	_, err := Vendor(context.Background(), sparkwingDir, "github.com/sparkwing-dev/sparks-core/templates")
	if err == nil {
		t.Fatal("expected refusal when destination already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' error, got %v", err)
	}
}

// TestVendor_EndToEnd exercises the full flow against a hermetic
// file-based module proxy: download from the cache, copy the tree, add
// the replace directive, and run go mod tidy. No network access.
func TestVendor_EndToEnd(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	const modPath = "example.com/fixturemod"
	const modVer = "v0.1.0"

	proxyDir := buildFileProxy(t, modPath, modVer)
	modCache := writableTempDir(t)

	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxyDir))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", modCache)
	t.Setenv("GOTOOLCHAIN", "local")

	sparkwingDir := t.TempDir()
	writeFile(t, filepath.Join(sparkwingDir, "go.mod"),
		"module consumer.example\n\ngo 1.26.0\n\nrequire "+modPath+" "+modVer+"\n")
	writeFile(t, filepath.Join(sparkwingDir, "use.go"),
		"package consumer\n\nimport _ \""+modPath+"\"\n")

	res, err := Vendor(context.Background(), sparkwingDir, modPath)
	if err != nil {
		t.Fatalf("Vendor: %v", err)
	}
	if res.Version != modVer {
		t.Errorf("resolved version = %q, want %q", res.Version, modVer)
	}

	hello := filepath.Join(sparkwingDir, VendoredDirName, "fixturemod", "hello.go")
	info, err := os.Stat(hello)
	if err != nil {
		t.Fatalf("expected vendored source at %s: %v", hello, err)
	}
	if info.Mode()&0o200 == 0 {
		t.Errorf("vendored file should be writable, got %v", info.Mode())
	}

	goMod, err := os.ReadFile(filepath.Join(sparkwingDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "replace "+modPath+" => ./sparks/fixturemod") {
		t.Errorf("replace directive missing from go.mod:\n%s", goMod)
	}
}

// buildFileProxy writes a minimal GOPROXY file tree serving one module
// version and returns the proxy root directory.
func buildFileProxy(t *testing.T, modPath, version string) string {
	t.Helper()
	proxyRoot := t.TempDir()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "go.mod"), "module "+modPath+"\n\ngo 1.26.0\n")
	writeFile(t, filepath.Join(src, "hello.go"), "package fixturemod\n\n// Hello is a fixture symbol.\nfunc Hello() string { return \"hi\" }\n")
	writeFile(t, filepath.Join(src, "spark.json"), `{"name":"fixturemod","description":"fixture","author":"test","packages":[{"path":".","description":"root"}]}`)

	escaped, err := module.EscapePath(modPath)
	if err != nil {
		t.Fatal(err)
	}
	vdir := filepath.Join(proxyRoot, filepath.FromSlash(escaped), "@v")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(vdir, "list"), version+"\n")
	writeFile(t, filepath.Join(vdir, version+".info"), `{"Version":"`+version+`","Time":"2024-01-01T00:00:00Z"}`)
	goModBytes, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(vdir, version+".mod"), string(goModBytes))

	var buf bytes.Buffer
	if err := modzip.CreateFromDir(&buf, module.Version{Path: modPath, Version: version}, src); err != nil {
		t.Fatalf("CreateFromDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(vdir, version+".zip"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return proxyRoot
}

// writableTempDir returns a temp dir whose cleanup restores write
// permissions first. The Go module cache extracts files read-only, so a
// plain t.TempDir cleanup fails to remove a cache rooted under it.
func writableTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sparks-modcache")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, werr error) error {
			if werr == nil {
				_ = os.Chmod(p, 0o755)
			}
			return nil
		})
		_ = os.RemoveAll(dir)
	})
	return dir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
