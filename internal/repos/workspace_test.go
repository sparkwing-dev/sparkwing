package repos

import (
	"os"
	"path/filepath"
	"testing"
)

func writeLocalSDK(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "module " + sdkModulePath + "\n\ngo 1.26\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeWork(t *testing.T, sparkwingDir string, uses ...string) {
	t.Helper()
	body := "go 1.26\n\nuse (\n\t.\n"
	for _, u := range uses {
		body += "\t" + u + "\n"
	}
	body += ")\n"
	if err := os.WriteFile(filepath.Join(sparkwingDir, "go.work"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSDKWorkspaceOverride_FindsALocalSDKUse(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app")
	writeSparkwingPin(t, repo, "v0.15.4")
	sdk := filepath.Join(base, "sparkwing")
	writeLocalSDK(t, sdk)
	sw := filepath.Join(repo, ".sparkwing")
	writeWork(t, sw, "../../sparkwing")

	got := SDKWorkspaceOverride(sw)
	want, err := filepath.EvalSymlinks(sdk)
	if err != nil {
		want = sdk
	}
	gotResolved, err := filepath.EvalSymlinks(got)
	if err != nil {
		gotResolved = got
	}
	if gotResolved != want {
		t.Fatalf("SDKWorkspaceOverride = %q, want %q", gotResolved, want)
	}
	if pin, _ := SDKPin(sw); pin != "v0.15.4" {
		t.Fatalf("pin should still read %q so callers can report both", "v0.15.4")
	}
}

func TestSDKWorkspaceOverride_EmptyWithoutAWorkspace(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "app")
	writeSparkwingPin(t, repo, "v0.17.25")
	if got := SDKWorkspaceOverride(filepath.Join(repo, ".sparkwing")); got != "" {
		t.Fatalf("SDKWorkspaceOverride = %q, want empty", got)
	}
}

func TestSDKWorkspaceOverride_IgnoresAWorkspaceThatDoesNotUseTheSDK(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app")
	writeSparkwingPin(t, repo, "v0.17.25")
	other := filepath.Join(base, "helper")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/helper\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sw := filepath.Join(repo, ".sparkwing")
	writeWork(t, sw, "../../helper")

	if got := SDKWorkspaceOverride(sw); got != "" {
		t.Fatalf("SDKWorkspaceOverride = %q, want empty for a non-SDK workspace", got)
	}
}
