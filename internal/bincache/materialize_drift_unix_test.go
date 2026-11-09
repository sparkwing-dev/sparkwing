//go:build !windows

package bincache

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMaterializePipelineRefusesChangedCompilerInputs(t *testing.T) {
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GOFLAGS", "-buildvcs=false -p=1")
	t.Setenv("SPARKWING_HOME", t.TempDir())
	dir := newPipelineDir(t)
	source := filepath.Join(dir, "main.go")
	original := "package main\nfunc main(){print(\"original\")}\n"
	writeFile(t, source, original)
	gate := filepath.Join(t.TempDir(), "once")
	writeFile(t, gate, "ready")
	wrapper := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = build ] && [ -f " + shQuote(gate) + " ]; then\nrm " + shQuote(gate) + "\nprintf 'package main\\nfunc main(){print(\"changed\")}\\n' > " + shQuote(source) + "\nfi\nexec " + shQuote(realGo) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapper+string(os.PathListSeparator)+os.Getenv("PATH"))
	lease, published, err := MaterializePipeline(t.Context(), dir, CompileOptions{Environment: os.Environ(), Diagnostics: io.Discard})
	if lease != nil {
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	if !errors.Is(err, ErrUnstableInputs) || published || lease != nil {
		t.Errorf("changed compiler inputs: lease=%v published=%t error=%v; want ErrUnstableInputs without publication", lease, published, err)
	}
	if _, err := os.Stat(gate); !os.IsNotExist(err) {
		t.Fatal("source edit did not reach the compiler")
	}
	writeFile(t, source, original)
	if out, built := materializeFixture(t, dir); out != "original" || !built {
		t.Fatalf("restored source = %q compiled=%t; want original and a rebuild", out, built)
	}
}
