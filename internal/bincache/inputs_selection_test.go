package bincache

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
)

func TestSelectedGoListPreservesCompiledInputDigest(t *testing.T) {
	for _, scenario := range []string{"embedded-dependency", "missing-import", "cgo"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("GOWORK", "off")
			t.Setenv("GOPROXY", "off")
			t.Setenv("GOFLAGS", "-trimpath -p=1")
			dir := newPipelineDir(t)
			lib := filepath.Join(dir, "lib")
			writeFile(t, filepath.Join(dir, "go.mod"), "module fixture\ngo 1.26\nrequire example.test/lib v0.0.0\nreplace example.test/lib => ./lib\n")
			writeFile(t, filepath.Join(lib, "go.mod"), "module example.test/lib\ngo 1.26\n")
			writeFile(t, filepath.Join(lib, "value.go"), "package lib\nimport _ \"embed\"\n//go:embed hidden/data\nvar Value string\n")
			writeFile(t, filepath.Join(lib, "hidden", "data"), "compiled asset")
			writeFile(t, filepath.Join(dir, "main.go"), "package main\nimport \"example.test/lib\"\nfunc main(){print(lib.Value)}\n")
			if scenario == "missing-import" {
				writeFile(t, filepath.Join(lib, "missing.go"), "package lib\nimport _ \"example.invalid/missing\"\n")
			}
			if scenario == "cgo" {
				t.Setenv("CGO_ENABLED", "1")
				writeFile(t, filepath.Join(lib, "native.go"), "package lib\nimport \"C\"\n")
				writeFile(t, filepath.Join(lib, "native.c"), "int native_value = 1;\n")
				writeFile(t, filepath.Join(lib, "native.h"), "extern int native_value;\n")
			}
			environment := pipelineBuildEnvironment(os.Environ())
			tool, err := gotoolchain.Executable(environment)
			if err != nil {
				t.Fatal(err)
			}
			full, err := compiledInputDigest(func() ([]byte, error) {
				args := append([]string{"list", "-e", "-deps", "-json"}, pipelineBuildFlags...)
				cmd := procenv.ControlCommand(environment, tool, append(args, ".")...)
				cmd.Dir = dir
				return cmd.Output()
			}, dir, "-trimpath -p=1", "off")
			if err != nil {
				t.Fatal(err)
			}
			_, selected, err := effectiveBuildInputs(t.Context(), dir, environment)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(full, selected) {
				t.Fatal("selected Go fields changed compiler input identity")
			}
		})
	}
}
