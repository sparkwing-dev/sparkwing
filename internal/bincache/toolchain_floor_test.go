package bincache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/testshell"
)

func compileFloorFixture(t *testing.T, setting, floor, buildBody string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s|%s\\n' \"$1\" \"$GOTOOLCHAIN\" >> '" + filepath.ToSlash(log) + "'\n" +
		"if [ \"$1\" = env ]; then printf '%s\\n' '" + setting + "' 'go1.26.6' 'off'; exit 0; fi\n" + buildBody
	testshell.Install(t, filepath.Join(dir, "go"), script)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/pipeline\ngo "+floor+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOTOOLCHAIN", setting)
	t.Setenv("GOWORK", "off")
	return dir, log
}

func TestCompilePipelineOverridesFixedPinForBuildOnly(t *testing.T) {
	dir, log := compileFloorFixture(t, "go1.26.6", "1.26.8", "exit 0\n")
	ctx := gotoolchain.WithSession(context.Background(), os.Environ(), func(string) {})
	if err := CompilePipeline(ctx, dir, filepath.Join(dir, "pipeline"), CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "build|go1.26.8") {
		t.Fatalf("build calls: %s", data)
	}
	if strings.Count(string(data), "env|") != 1 {
		t.Fatalf("probe calls: %s", data)
	}
	if os.Getenv("GOTOOLCHAIN") != "go1.26.6" {
		t.Fatal("changed process runtime pin")
	}
}

func TestCompilePipelineExplainsLocalDependencyFloor(t *testing.T) {
	dir, _ := compileFloorFixture(t, "local", "1.26.0", "printf 'go: example.com/dependency requires go >= 1.27.2 (running go 1.26.6; GOTOOLCHAIN=local)\\n' >&2\nexit 1\n")
	err := CompilePipeline(context.Background(), dir, filepath.Join(dir, "pipeline"), CompileOptions{Environment: os.Environ(), Diagnostics: os.Stderr})
	var compileErr *compileError
	var floorErr *gotoolchain.Error
	if !errors.As(err, &compileErr) || !errors.As(err, &floorErr) {
		t.Fatalf("compile error = %v", err)
	}
	for _, fragment := range []string{"1.27.2", "1.26.6", "GOTOOLCHAIN=local", "the GOTOOLCHAIN environment variable", "unset GOTOOLCHAIN"} {
		if !strings.Contains(string(compileErr.Output), fragment) {
			t.Errorf("missing %q in compile output: %s", fragment, compileErr.Output)
		}
	}
}
