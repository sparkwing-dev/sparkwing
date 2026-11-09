package jobs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDeployCLIReusesOnlyStagingVerifiedBytes(t *testing.T) {
	for _, scenario := range []string{"hit", "source mismatch", "platform mismatch", "missing", "corrupt", "invalid digest", "no receipt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFakeDeploy(t)
			staging := f.deployer(false)
			if err := staging.buildCLI(t.Context()); err != nil {
				t.Fatal(err)
			}
			gate := stagingGateRecord{Images: f.candidate(), CLIDigest: staging.cacheCLI(t.Context()), CLIPlatform: runtime.GOOS + "/" + runtime.GOARCH}
			if gate.CLIDigest == "" {
				t.Fatal("missing CLI digest")
			}
			cached := deployCLICache(gate.CLIDigest)
			if err := removeDeployDir(staging.tmp); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "platform mismatch":
				gate.CLIPlatform = "other/arch"
			case "source mismatch":
				gate.Images.Commit = strings.Repeat("a", 40)
			case "missing":
				if err := os.Remove(cached); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(cached, []byte("wrong CLI"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "invalid digest":
				gate.CLIDigest = "../../bad"
			case "no receipt":
				gate = stagingGateRecord{}
			}
			production := f.deployer(false)
			production.gate = gate
			f.calls = nil
			if err := production.productionCLI(t.Context(), f.head); err != nil {
				t.Fatal(err)
			}
			built := false
			for _, call := range f.calls {
				built = built || strings.Contains(call, "go build -o")
			}
			if built != (scenario != "hit") {
				t.Fatalf("rebuilt=%v for %s", built, scenario)
			}
			data, err := os.ReadFile(production.cliBin())
			if err != nil || string(data) != "candidate CLI" {
				t.Fatalf("production CLI=%q, %v", data, err)
			}
			if info, err := os.Stat(production.cliBin()); err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("private executable: %v, %v", info, err)
			}
			if _, err := os.Stat(filepath.Join(production.tmp, "cli-home")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeployCLIHandoffExpiresWithDeployScratch(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	if err := d.buildCLI(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := deployCLICache(d.cacheCLI(t.Context()))
	old := f.clock.Add(-4 * time.Hour)
	if err := os.Chtimes(filepath.Dir(path), old, old); err != nil {
		t.Fatal(err)
	}
	if err := sweepStaleDeployDirs(os.TempDir(), f.clock, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired handoff remains: %v", err)
	}
}

func TestDeployCLIHelpDescribesVerifiedReuse(t *testing.T) {
	for _, proof := range []string{"same source SHA", "native platform", "binary digest", "rebuilding on a missing or mismatched handoff"} {
		if !strings.Contains((Deploy{}).Help(), proof) {
			t.Errorf("deploy help omits %q", proof)
		}
	}
}

func TestDeployFailedGateDoesNotPublishCLI(t *testing.T) {
	f := newFakeDeploy(t)
	f.failRuns["run-deploy-smoke-staging"] = true
	rec, err := f.deployer(false).stagingGate(t.Context(), f.candidate())
	if err == nil || rec.CLIDigest != "" {
		t.Fatalf("failed gate published CLI: %+v, %v", rec, err)
	}
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), deployDirPrefix+"cli-") {
			t.Fatalf("failed gate retained %s", entry.Name())
		}
	}
}
