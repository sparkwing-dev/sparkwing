//go:build !windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"golang.org/x/sys/unix"

	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
)

func TestSDKCheckRejectsFIFO(t *testing.T) {
	if root := os.Getenv("SPARKWING_UPDATE_FIFO_FIXTURE"); root != "" {
		isolateUpdateTests(t)
		t.Chdir(root)
		report := readPinSummary(filepath.Join(root, ".sparkwing"))
		if report.Runs != "" || report.Refusal == "" {
			t.Fatalf("FIFO module misreported: %+v", report)
		}
		if _, err := os.Stat(os.Getenv("SPARKWING_HOME")); !os.IsNotExist(err) {
			t.Fatal("FIFO check wrote state")
		}
		return
	}
	dir := sdkUpdateFixture(t, "")
	path := filepath.Join(dir, "go.mod")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSDKCheckRejectsFIFO$")
	cmd.Env = append(os.Environ(), "SPARKWING_UPDATE_FIFO_FIXTURE="+filepath.Dir(dir))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("FIFO check blocked or failed: %v %s", err, out)
	}
}

func TestInstalledArtifactInspectionRejectsFIFO(t *testing.T) {
	if path := os.Getenv("SPARKWING_UPDATE_ARTIFACT_FIFO"); path != "" {
		identity := installedArtifactIdentity(path)
		if identity.Version != "" || identity.Revision != "" || identity.Dirty != nil {
			t.Fatalf("special file has invented identity: %+v", identity)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "artifact")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInstalledArtifactInspectionRejectsFIFO$")
	cmd.Env = append(os.Environ(), "SPARKWING_UPDATE_ARTIFACT_FIFO="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("artifact FIFO inspection blocked: %v %s", err, out)
	}
}

func TestSDKPinInspectionRejectsUnsafeInputsProperty(t *testing.T) {
	inputs := []struct {
		sdk  engineid.SDKKind
		name string
	}{
		{engineid.GoSDK, "go.mod"},
		{engineid.GoSDK, "go.sum"},
		{engineid.TypeScriptSDK, "package.json"},
		{engineid.TypeScriptSDK, "package-lock.json"},
		{engineid.TypeScriptSDK, "npm-shrinkwrap.json"},
	}
	property := func(selection, kind uint8, payload string) bool {
		dir := t.TempDir()
		input := inputs[int(selection)%len(inputs)]
		files := map[string]string{
			"go.mod":            "module fixture\nrequire github.com/sparkwing-dev/sparkwing v1.2.3\n",
			"go.sum":            "",
			"package.json":      `{"dependencies":{"@sparkwing/sdk":"1.2.3"}}`,
			"package-lock.json": `{"lockfileVersion":3,"packages":{"":{"dependencies":{"@sparkwing/sdk":"1.2.3"}},"node_modules/@sparkwing/sdk":{"version":"1.2.3"}}}`,
		}
		for name, body := range files {
			if name != input.name {
				writeRepoFile(t, filepath.Join(dir, name), body)
			}
		}
		path := filepath.Join(dir, input.name)
		switch kind % 4 {
		case 0, 3:
			target := filepath.Join(dir, "target")
			if kind%4 == 0 {
				writeRepoFile(t, target, payload)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		case 1:
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
		case 2:
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		report := gatherSDKPinWith(Info{Project: InfoProject{Found: true, SparkwingDir: dir}}, input.sdk, engine.ReadPin, engine.VersionForPin)
		return report.Runs == "" && strings.Contains(report.Refusal, "regular file")
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 100}); err != nil {
		t.Fatal(err)
	}
}
