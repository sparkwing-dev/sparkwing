package bincache

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func materializeFixture(t *testing.T, dir string) (string, bool) {
	t.Helper()
	lease, built, err := MaterializePipeline(t.Context(), dir, CompileOptions{Environment: os.Environ(), Diagnostics: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	binary, err := lease.Install(filepath.Join(t.TempDir(), "pipeline"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), binary.Path()).CombinedOutput()
	if err != nil {
		t.Fatalf("run pipeline: %v: %s", err, out)
	}
	return string(out), built
}

func TestGoEnvFileChangesRebuildPipeline(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("exercises the native GOAMD64 setting")
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-buildvcs=false -p=1")
	t.Setenv("GOAMD64", "")
	t.Setenv("SPARKWING_HOME", t.TempDir())
	config := filepath.Join(t.TempDir(), "goenv")
	t.Setenv("GOENV", config)
	dir := newPipelineDir(t)
	writeFile(t, config, "GOAMD64=v1\n")
	if _, built := materializeFixture(t, dir); !built {
		t.Fatal("first writer did not compile")
	}
	if _, built := materializeFixture(t, dir); built {
		t.Fatal("matching Go environment did not reuse its binary")
	}
	t.Setenv("HOME", t.TempDir())
	if _, built := materializeFixture(t, dir); built {
		t.Fatal("another private home with matching effective inputs rebuilt")
	}
	writeFile(t, config, "GOAMD64=v2\n")
	if _, built := materializeFixture(t, dir); !built {
		t.Fatal("changed effective Go environment reused the first writer's binary")
	}
}

func TestIgnoredCompiledInputsRebuildPipeline(t *testing.T) {
	for _, ignore := range []string{"repository", "global", "embedded", "node_modules", "replacement"} {
		t.Run(ignore, func(t *testing.T) {
			t.Setenv("GOWORK", "off")
			t.Setenv("GOFLAGS", "-buildvcs=false -p=1")
			t.Setenv("SPARKWING_HOME", t.TempDir())
			dir := newGitPipelineDir(t)
			input := filepath.Join(dir, "generated.go")
			before, after := "package main\nconst value = \"first\"\n", "package main\nconst value = \"second\"\n"
			writeFile(t, filepath.Join(dir, "main.go"), "package main\nfunc main(){print(value)}\n")
			if ignore == "embedded" || ignore == "node_modules" {
				assets := "assets"
				if ignore == "node_modules" {
					assets = "node_modules"
				}
				input = filepath.Join(dir, assets, "payload")
				before, after = "first", "second"
				writeFile(t, filepath.Join(dir, "main.go"), "package main\nimport _ \"embed\"\n//go:embed "+assets+"/payload\nvar value string\nfunc main(){print(value)}\n")
			}
			if ignore == "replacement" {
				lib := filepath.Join(dir, "lib")
				input = filepath.Join(lib, "generated.go")
				writeFile(t, filepath.Join(lib, "go.mod"), "module example.test/lib\ngo 1.26\n")
				before, after = "package lib\nconst Value = \"first\"\n", "package lib\nconst Value = \"second\"\n"
				writeFile(t, filepath.Join(dir, "go.mod"), "module fixture\ngo 1.26\nrequire example.test/lib v0.0.0\nreplace example.test/lib => ./lib\n")
				writeFile(t, filepath.Join(dir, "main.go"), "package main\nimport \"example.test/lib\"\nfunc main(){print(lib.Value)}\n")
			}
			if ignore == "global" {
				excludes := filepath.Join(t.TempDir(), "excludes")
				writeFile(t, excludes, "generated.go\n")
				config := filepath.Join(t.TempDir(), "gitconfig")
				writeFile(t, config, "[core]\nexcludesFile = "+filepath.ToSlash(excludes)+"\n")
				t.Setenv("GIT_CONFIG_GLOBAL", config)
			} else {
				writeFile(t, filepath.Join(dir, ".gitignore"), "generated.go\nassets/\nlib/\n")
			}
			writeFile(t, input, before)
			if out, built := materializeFixture(t, dir); out != "first" || !built {
				t.Fatalf("first build = %q, %t", out, built)
			}
			writeFile(t, input, after)
			if out, built := materializeFixture(t, dir); out != "second" || !built {
				t.Fatalf("changed compiled input = %q, built=%t; want second and a rebuild", out, built)
			}
		})
	}
}

func TestSharedCacheFirstWriterIgnoresPrivateConfig(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-buildvcs=false -p=1")
	ownerCache := filepath.Join(t.TempDir(), "cache", "pipelines")
	caller := t.TempDir()
	if err := os.MkdirAll(ownerCache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(caller, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ownerCache, filepath.Join(caller, "cache", "pipelines")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("SPARKWING_HOME", caller)
	writeCacheConfig(t, "cache:\n  max_bytes: private-invalid-limit\n")
	dir := newPipelineDir(t)
	if _, built := materializeFixture(t, dir); !built {
		t.Fatal("first shared writer did not compile")
	}
	if _, built := materializeFixture(t, dir); built {
		t.Fatal("shared cache hit rebuilt")
	}
}

func TestSharedCacheWritersLeavePruningToOwner(t *testing.T) {
	owner, caller := t.TempDir(), t.TempDir()
	t.Setenv("SPARKWING_HOME", owner)
	writeCacheConfig(t, "cache:\n  max_bytes: 0\n  max_entries: 0\n")
	first, err := PipelineEntry("00000000-00000001")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := first.AcquireOrMaterialize(t.Context(), func(path string) error { return os.WriteFile(path, []byte("first"), 0o700) })
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(caller, "config.yaml"), "cache:\n  max_entries: 1\n")
	if err := os.Chmod(filepath.Join(caller, "config.yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(caller, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(owner, "cache", "pipelines"), filepath.Join(caller, "cache", "pipelines")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("SPARKWING_HOME", caller)
	t.Setenv("SPARKWING_CONFIG", filepath.Join(caller, "config.yaml"))
	second, err := PipelineEntry("00000000-00000002")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err = second.AcquireOrMaterialize(t.Context(), func(path string) error { return os.WriteFile(path, []byte("second"), 0o700) })
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.binaryPath()); err != nil {
		t.Fatalf("private caller limits pruned the owner's entry: %v", err)
	}
}
