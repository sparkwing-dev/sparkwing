package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var nativeReleaseFixtures sync.Map

func releaseFixtureForPlatform(t *testing.T, version string) []byte {
	t.Helper()
	if runtime.GOOS != "windows" {
		return releaseFixture(version)
	}
	if raw, ok := nativeReleaseFixtures.Load(version); ok {
		return raw.([]byte)
	}
	dir := t.TempDir()
	source := fmt.Sprintf(`package main
import ("fmt"; "os"; "strings")
func main() {
 if len(os.Args)>1 && os.Args[1]=="version" { fmt.Println(%q); return }
 fmt.Println("fixture argv:",strings.Join(os.Args[1:]," "))
 fmt.Println("fixture active:",os.Getenv("SPARKWING_TOOLCHAIN_ACTIVE"))
 os.Exit(7)
}
`, `{"cli":{"installed":"`+version+`"}}`)
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fixture.exe")
	cmd := exec.Command("go", "build", "-o", bin, path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile native signed-release fixture: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	nativeReleaseFixtures.Store(version, raw)
	return raw
}
