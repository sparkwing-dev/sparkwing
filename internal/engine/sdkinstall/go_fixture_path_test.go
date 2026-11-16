package sdkinstall

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRealGoSDKFixtureIgnoresUnselectedCompilerDirectory(t *testing.T) {
	bin := t.TempDir()
	name := "clang"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	compiler := filepath.Join(bin, name)
	if err := os.WriteFile(compiler, []byte("unused compiler"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CGO_ENABLED", "0")
	dir := realBuildPolicyFixture(t)
	first := runBuildPolicyInstall(t, dir, "")
	assertBuildPolicyOutput(t, first, "first")
	if first.CachedExecutable == "" {
		t.Fatal("fixture installation did not use the binary cache")
	}
	if err := os.WriteFile(compiler, []byte("changed unused compiler bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	second := runBuildPolicyInstall(t, dir, "")
	assertBuildPolicyOutput(t, second, "first")
	if first.CachedExecutable != second.CachedExecutable {
		t.Fatal("unselected ambient compiler changed the fixture's cache entry")
	}
}
