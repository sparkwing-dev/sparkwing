package engine

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/gotoolchain"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

func TestInstallationHandoffCopiesVerifiedBytesAndRejectsMismatches(t *testing.T) {
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
	settings := procenv.ControlCommand(values, tool, "env", "-json")
	settings.Dir = dir
	settingsBody, err := settings.Output()
	if err != nil {
		t.Fatal(err)
	}
	inputs := &bincache.InputWitness{States: map[string]string{filepath.Join(dir, "absent"): "absent"}, Directory: dir, Environment: values, Tool: tool, Settings: settingsBody}
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
	request := SDKInstallRequest{Engine: engineid.Identity{Pin: pin, Version: "v0.20.0", ManifestDigest: "selected-engine"}, Directory: dir, SDK: engineid.GoSDK, ExecutableDirectory: t.TempDir()}
	command := SDKCommand{Directory: dir, Command: []string{original.Path()}, CachedExecutable: lease.CachedPath()}
	request.Handoff = WriteSDKHandoff(dir, request, environment, command, inputs)
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
			var receipt installationHandoff
			if err := json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			switch test {
			case "identity":
				receipt.Engine.ManifestDigest = "different"
			case "engine":
				receipt.Engine.Version = "v0.21.0"
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
			got, reused := restoreSDKHandoff(t.Context(), dir, current, environment)
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
			} else if files, err := os.ReadDir(current.ExecutableDirectory); err != nil || len(files) != 0 {
				t.Fatalf("rejected handoff retained executable: %v: %v", files, err)
			}
		})
	}
}

func TestInstallSDKRestoresHandoffBeforeRunningEngine(t *testing.T) {
	t.Setenv("SPARKWING_HOME", t.TempDir())
	key := testSigningKey(t)
	target := releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	version := "v0.20.0"
	bundle := signedBundle(t, key, version, target, fixtureExecutableSource(t, version, target, installerFixtureSource))
	release, err := VerifyRelease(version, target, bundle, signingKeys(key))
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := NewResolver(Config{TrustedKeys: signingKeys(key), CacheDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeInstallPin(t, dir, engineid.GoSDK)
	pin, err := ReadPin(dir, engineid.GoSDK)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.storeVerified(t.Context(), pin, release)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "installer-ran")
	request := SDKInstallRequest{Engine: resolved.Identity, Directory: dir, SDK: engineid.GoSDK, ExecutableDirectory: t.TempDir(), Environment: append(os.Environ(), "INSTALL_FAILURE=1", "INSTALL_MARKER="+marker)}
	environment, err := procenv.Build(procenv.Install, procenv.Base{Local: true, Captured: request.Environment, Masker: secrets.NewMasker()}, procenv.Runtime{})
	if err != nil {
		t.Fatal(err)
	}
	values := slices.Collect(environment.Values())
	tool, err := gotoolchain.Executable(values)
	if err != nil {
		t.Fatal(err)
	}
	settings := procenv.ControlCommand(values, tool, "env", "-json")
	settings.Dir = dir
	settingsBody, err := settings.Output()
	if err != nil {
		t.Fatal(err)
	}
	inputs := &bincache.InputWitness{States: map[string]string{filepath.Join(dir, "absent"): "absent"}, Directory: dir, Environment: values, Tool: tool, Settings: settingsBody}
	entry, err := bincache.PipelineEntry("00000003-00000004")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := entry.AcquireOrMaterialize(t.Context(), func(path string) error { return os.WriteFile(path, []byte("verified-reused-build"), 0o700) })
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	original, err := lease.Install(filepath.Join(t.TempDir(), "pipeline"))
	if err != nil {
		t.Fatal(err)
	}
	request.Handoff = WriteSDKHandoff(dir, request, environment, SDKCommand{Directory: dir, Command: []string{original.Path()}, CachedExecutable: lease.CachedPath()}, inputs)
	if request.Handoff == "" {
		t.Fatal("no invocation handoff")
	}
	command, err := resolved.InstallSDK(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := command.Close(); err != nil {
			t.Error(err)
		}
	})
	if !command.Reused {
		t.Fatal("verified handoff was rebuilt")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("engine installer executed: %v", err)
	}
	bytes, err := os.ReadFile(command.Command[0])
	if err != nil || string(bytes) != "verified-reused-build" {
		t.Fatalf("reused bytes %q: %v", bytes, err)
	}
	if err := command.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(command.Command[0]); !os.IsNotExist(err) {
		t.Fatalf("restored executable cleanup: %v", err)
	}
}
