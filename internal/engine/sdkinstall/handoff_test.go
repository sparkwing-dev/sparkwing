package sdkinstall

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestInstallationHandoffCopiesVerifiedBytesAndRejectsMismatches(t *testing.T) {
	installNativeToolFixture(t)
	t.Setenv("SPARKWING_HOME", t.TempDir())
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	values := slices.Collect(environment.Values())
	tool, err := gotoolchain.Executable(values)
	if err != nil {
		t.Fatal(err)
	}
	inputs := &bincache.InputWitness{States: map[string]string{filepath.Join(dir, "absent"): "absent"}, Directory: dir, Environment: values, Tool: tool, Settings: []byte(`{"GOVERSION":"go1.26.9"}`)}
	entry, err := bincache.PipelineEntry("00000001-00000002")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(t.Context(), func(path string) error { return os.WriteFile(path, []byte("verified-first-build"), 0o700) })
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	original, err := lease.Install(filepath.Join(t.TempDir(), "pipeline"))
	if err != nil {
		t.Fatal(err)
	}
	pin := engineid.Pin{SDK: engineid.GoSDK, Version: "v0.20.0"}
	request := engine.SDKInstallRequest{Directory: dir, SDK: engineid.GoSDK, ExecutableDirectory: t.TempDir()}
	command := engine.SDKCommand{Directory: dir, Command: []string{original.Path()}, CachedExecutable: lease.CachedPath()}
	request.Handoff = writeHandoff("v0.20.0", dir, pin, request, environment, command, inputs)
	if request.Handoff == "" {
		t.Fatal("unchanged verified installation produced no handoff")
	}
	body, err := os.ReadFile(request.Handoff)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"unchanged", "identity", "engine", "environment", "source", "digest", "cache", "missing", "disabled", "mode"} {
		t.Run(test, func(t *testing.T) {
			current := request
			current.ExecutableDirectory = t.TempDir()
			version := "v0.20.0"
			var receipt installationHandoff
			if err := json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			switch test {
			case "identity":
				receipt.Engine.ManifestDigest = "different"
			case "engine":
				version = "v0.21.0"
			case "environment":
				receipt.Environment = "different"
			case "source":
				receipt.Directory = t.TempDir()
			case "digest":
				receipt.Digest = "different"
			case "cache":
				receipt.CachedExecutable = filepath.Join(t.TempDir(), "pipeline")
			case "missing":
				current.Handoff = filepath.Join(t.TempDir(), "absent")
			case "disabled":
				receipt.Inputs.Disabled = true
			case "mode":
				current.NoBincache = true
			}
			changed, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(request.Handoff, changed, 0o600); err != nil {
				t.Fatal(err)
			}
			got, reused := restoreHandoff(t.Context(), version, dir, pin, current, environment)
			if reused != (test == "unchanged") {
				t.Fatalf("reused=%t", reused)
			}
			if reused {
				f, err := os.Open(got.Command[0])
				if err != nil {
					t.Fatal(err)
				}
				value, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil || string(value) != "verified-first-build" || got.Command[0] == original.Path() {
					t.Fatalf("copy=%q err=%v", value, err)
				}
			} else if _, err := os.Stat(filepath.Join(current.ExecutableDirectory, "pipeline")); !os.IsNotExist(err) {
				t.Fatalf("rejected handoff retained executable: %v", err)
			}
		})
	}
}
