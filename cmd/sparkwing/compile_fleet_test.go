package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCompileAndExecFleetCachedPathReturnsToSnapshotOwner(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_FLEET_CACHED_CHILD") == "1" {
		err := compileAndExec(os.Getenv("SPARKWING_TEST_FLEET_COMPILE_DIR"), nil,
			append(os.Environ(), "SPARKWING_FLEET=1", "GOWORK=off"), compileOptions{NoUpdate: true})
		var cliErr *cliError
		if !errors.As(err, &cliErr) || cliErr.code != 17 {
			os.Exit(19)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/fleetcached\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nimport \"os\"\nfunc main() { os.Exit(17) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompileAndExecFleetCachedPathReturnsToSnapshotOwner$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_FLEET_CACHED_CHILD=1",
		"SPARKWING_TEST_FLEET_COMPILE_DIR="+dir,
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), "home"),
		"GOWORK=off",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fleet cached wrapper bypassed its owner: %v\n%s", err, out)
	}
}

func TestCompileAndExecFleetSuccessfulCachedPathReturnsToSnapshotOwner(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_FLEET_CACHED_SUCCESS") == "1" {
		err := compileAndExec(os.Getenv("SPARKWING_TEST_FLEET_COMPILE_DIR"), nil,
			append(os.Environ(), "SPARKWING_FLEET=1", "GOWORK=off"), compileOptions{NoUpdate: true})
		if err != nil {
			os.Exit(19)
		}
		if err := os.WriteFile(os.Getenv("SPARKWING_TEST_FLEET_RETURNED"), []byte("returned"), 0o600); err != nil {
			os.Exit(20)
		}
		os.Exit(0)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/fleetcachedsuccess\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	returned := filepath.Join(t.TempDir(), "returned")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompileAndExecFleetSuccessfulCachedPathReturnsToSnapshotOwner$")
	cmd.Env = append(os.Environ(),
		"SPARKWING_TEST_FLEET_CACHED_SUCCESS=1",
		"SPARKWING_TEST_FLEET_COMPILE_DIR="+dir,
		"SPARKWING_TEST_FLEET_RETURNED="+returned,
		"SPARKWING_HOME="+filepath.Join(t.TempDir(), "home"),
		"GOWORK=off",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("successful fleet cached wrapper failed: %v\n%s", err, out)
	}
	if body, err := os.ReadFile(returned); err != nil || string(body) != "returned" {
		t.Fatalf("snapshot owner did not regain control after successful child: %q, %v", body, err)
	}
}
