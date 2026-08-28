package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestKindE2EPipelineIsRegisteredAndBounded(t *testing.T) {
	registration, ok := sparkwing.Lookup("kind-e2e")
	if !ok {
		t.Fatal("kind-e2e is not registered")
	}
	plan, err := registration.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "kind-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	nodes := plan.Nodes()
	if len(nodes) != 1 || nodes[0].ID() != "kind-e2e" {
		t.Fatalf("kind-e2e nodes = %v, want [kind-e2e]", nodeIDs(nodes))
	}
	if got := nodes[0].TimeoutDuration(); got != 40*time.Minute {
		t.Fatalf("kind-e2e timeout = %s, want 40m", got)
	}
}

func TestKindE2ECommandCancellationRunsCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	markerDir := t.TempDir()
	readyMarker := filepath.Join(markerDir, "ready")
	cleanupMarker := filepath.Join(markerDir, "cleanup")
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", `
trap 'printf cleanup >"$CLEANUP_MARKER"; exit 143' TERM
printf ready >"$READY_MARKER"
while :; do sleep 1; done
`)
	cmd.Env = append(os.Environ(), "READY_MARKER="+readyMarker, "CLEANUP_MARKER="+cleanupMarker)
	configureKindE2ECommand(cmd)
	if cmd.Cancel == nil || cmd.WaitDelay != 90*time.Second {
		t.Fatal("Kind command cancellation is not bounded and graceful")
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	readyDeadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(readyMarker); err == nil {
			break
		}
		if time.Now().After(readyDeadline) {
			_ = cmd.Process.Kill()
			t.Fatal("Kind command did not become ready for cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("Kind command did not honor cancellation")
	}
	if got, err := os.ReadFile(cleanupMarker); err != nil || string(got) != "cleanup" {
		t.Fatalf("Kind cancellation cleanup marker = %q, %v", got, err)
	}
}

func TestKindE2EPreflightFailsBeforeWorkWithoutDocker(t *testing.T) {
	bin := t.TempDir()
	linkTool(t, bin, "dirname")
	result := runKindScript(t, bin)
	if result.err == nil {
		t.Fatal("preflight passed without docker")
	}
	if !strings.Contains(result.output, "required command 'docker' is not installed") {
		t.Fatalf("output = %q, want missing-docker guidance", result.output)
	}
}

func TestKindE2EPreflightFailsBeforeWorkWhenDaemonIsDown(t *testing.T) {
	bin := t.TempDir()
	linkTool(t, bin, "dirname")
	writeStub(t, bin, "docker", "exit 1\n")
	for _, name := range []string{"kind", "kubectl", "helm", "curl", "jq", "git", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKindScript(t, bin)
	if result.err == nil {
		t.Fatal("preflight passed with a dead Docker daemon")
	}
	if !strings.Contains(result.output, "Docker daemon is unavailable") {
		t.Fatalf("output = %q, want dead-daemon guidance", result.output)
	}
	if strings.Contains(result.output, "building dashboard") || strings.Contains(result.output, "creating Kind cluster") {
		t.Fatalf("preflight performed work before failing: %q", result.output)
	}
}

func TestKindE2EPreflightOnlyProbesTools(t *testing.T) {
	bin := t.TempDir()
	linkTool(t, bin, "dirname")
	record := filepath.Join(t.TempDir(), "calls")
	writeStub(t, bin, "docker", "printf 'docker %s\\n' \"$*\" >>\"$CALL_RECORD\"\n")
	for _, name := range []string{"kind", "kubectl", "helm"} {
		writeStub(t, bin, name, "printf '"+name+" %s\\n' \"$*\" >>\"$CALL_RECORD\"\n")
	}
	for _, name := range []string{"curl", "jq", "git", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKindScriptWithEnv(t, bin, "CALL_RECORD="+record)
	if result.err != nil {
		t.Fatalf("preflight with healthy stubs: %v\n%s", result.err, result.output)
	}
	if !strings.Contains(result.output, "preflight passed") {
		t.Fatalf("output = %q, want success verdict", result.output)
	}
	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	want := "docker info\ndocker buildx version\nkind version\nkubectl version --client\nhelm version --short\n"
	if string(calls) != want {
		t.Fatalf("preflight calls = %q, want %q", calls, want)
	}
}

func TestKindE2EDeletesAClusterLeftByFailedCreation(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"cat", "chmod", "cp", "dirname", "grep", "mkdir", "touch"} {
		linkTool(t, bin, name)
	}
	writeStub(t, bin, "bash", "exit 0\n")
	writeStub(t, bin, "docker", "exit 0\n")
	writeStub(t, bin, "kubectl", "exit 0\n")
	writeStub(t, bin, "helm", "exit 0\n")
	for _, name := range []string{"curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	writeStub(t, bin, "git", `
if [ "$1" = "clone" ]; then
  mkdir -p "$4"
elif [ "$1" = "-C" ] && [ "$3" = "rev-parse" ]; then
  printf '0123456789abcdef0123456789abcdef01234567\n'
fi
`)
	calls := filepath.Join(t.TempDir(), "kind-calls")
	writeStub(t, bin, "kind", `
printf 'kind %s\n' "$*" >>"$KIND_CALLS"
case "$1 $2" in
  "create cluster") exit 1 ;;
esac
`)
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	result := runKindScriptFullWithEnv(t, bin,
		"KIND_CALLS="+calls,
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
	)
	if result.err == nil {
		t.Fatal("Kind creation unexpectedly passed")
	}
	if !strings.Contains(result.output, "collecting failure diagnostics") {
		t.Fatalf("output = %q, want diagnostics after partial cluster creation", result.output)
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	create := strings.Index(got, "kind create cluster --name sparkwing-e2e")
	remove := strings.Index(got, "kind delete cluster --name sparkwing-e2e")
	if create < 0 || remove < create {
		t.Fatalf("Kind calls did not delete the partial owned cluster in order:\n%s", got)
	}
}

func TestKindE2EOwnsReleaseImagesAndFailureEvidence(t *testing.T) {
	script := readHostedCIFile(t, "bin/kind-e2e.sh")
	componentBlock := between(t, script, "components=(\n", ")\ndockerfiles=(")
	got := strings.Fields(componentBlock)
	want := make([]string, len(buildImagesComponents))
	for i, component := range buildImagesComponents {
		want[i] = component.name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Kind images = %v, release images = %v", got, want)
	}
	for _, marker := range []string{
		"docker buildx build",
		"kind load docker-image",
		"helm install",
		"invalid webhook returned $invalid_webhook_status, want 401",
		"sha256=0000000000000000000000000000000000000000000000000000000000000000",
		".runs | length == 0",
		"X-Hub-Signature-256",
		"/webhooks/github/${pipeline}",
		"/api/v1/tokens",
		"/api/v1/agents",
		"/logs/prove-controller-runner-logs",
		"/cancel",
		"/retry?full=1",
		"rollout restart \"deployment/$runner_deployment\"",
		"does not identify replacement runner pod",
		".status.readyReplicas == 1",
		"rollout restart \"deployment/$controller_deployment\"",
		"sort_by(.metadata.creationTimestamp)",
		"retry node output $retry_output does not match retry run $retry_run",
		"web_static_path",
		"referenced web static asset was empty",
		"controller PVC was not retained across uninstall",
		"helm get manifest",
		"get events --sort-by=.metadata.creationTimestamp",
		"logs \"$pod\" --all-containers",
		"kind export logs",
		"kind delete cluster --name \"$cluster_name\"",
	} {
		if !strings.Contains(script, marker) {
			t.Errorf("Kind harness is missing contract marker %q", marker)
		}
	}
	if strings.Contains(script, "docker push") || strings.Contains(script, "kind create cluster --image") {
		t.Fatal("Kind harness gained a registry push or an unowned node-image override")
	}
	if strings.Contains(script, `post_runner_claim" != "$success_claim`) {
		t.Fatal("runner restart proof compares per-claim nonce values instead of the replacement pod hostname")
	}
	if strings.Contains(script, "<!DOCTYPE html") || strings.Contains(script, "logs, and dashboard") {
		t.Fatal("Kind harness claims functional dashboard coverage from an HTML shell")
	}
	if got := strings.Count(script, `start_forward "$controller_service" 80`); got != 4 {
		t.Fatalf("controller forward starts = %d, want bootstrap, authenticated, restarted, and reinstalled", got)
	}
	invalidWebhook := strings.Index(script, "kind-e2e: proving invalid webhook authentication")
	validWebhook := strings.Index(script, "send_webhook kind-success")
	if invalidWebhook < 0 || validWebhook < 0 || invalidWebhook > validWebhook {
		t.Fatal("Kind harness does not reject an invalid HMAC before its first valid webhook")
	}
	fixture := readHostedCIFile(t, "testdata/kind-e2e/repo/.sparkwing/jobs/kind.go")
	for _, marker := range []string{
		"sparkwing.Produces[string]",
		"runID: rc.RunID",
		"sparkwing-kind-e2e-success run_id=%s",
		"return j.runID, nil",
	} {
		if !strings.Contains(fixture, marker) {
			t.Errorf("Kind fixture is missing causal retry marker %q", marker)
		}
	}
}

func TestHostedKindE2EIsPathScopedPinnedAndReadOnly(t *testing.T) {
	body := readHostedCIFile(t, ".github/workflows/kind-e2e.yaml")
	requireWorkflowText(t, body,
		"  pull_request:\n    paths: &kind_paths\n",
		"  push:\n    branches: [main]\n    paths: *kind_paths\n",
		"permissions:\n  contents: read\n",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0",
		"actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0",
		"node-version: \"20\"",
		"cache-dependency-path: web/package-lock.json",
		"go install sigs.k8s.io/kind@v0.32.0",
		"go install k8s.io/kubectl/cmd/kubectl@v0.36.1",
		"go install helm.sh/helm/v4/cmd/helm@v4.2.4",
		"run: '\"$RUNNER_TEMP/sparkwing\" run kind-e2e'",
		"if: failure() || cancelled()",
		"actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02 # v4.6.2",
	)
	for _, path := range []string{
		`- ".dockerignore"`,
		`- ".sparkwing/**"`,
		`- "bin/build-web.sh"`,
		`- "bin/kind-e2e.sh"`,
		`- "build/**"`,
		`- "charts/**"`,
		`- "cmd/**"`,
		`- "internal/**"`,
		`- "pkg/**"`,
		`- "sparkwing/**"`,
		`- "testdata/kind-e2e/**"`,
		`- "web/**"`,
	} {
		if !strings.Contains(body, path) {
			t.Errorf("Kind workflow path scope is missing %q", path)
		}
	}
	if strings.Contains(body, ": write") || strings.Contains(body, "docker push") {
		t.Fatal("Kind workflow gained write permission or a registry push")
	}
	if strings.Contains(body, "\n        if: failure()\n") {
		t.Fatal("Kind workflow drops diagnostics when a run is cancelled")
	}
	nodeSetup := strings.Index(body, "actions/setup-node@")
	harnessRun := strings.Index(body, `run: '"$RUNNER_TEMP/sparkwing" run kind-e2e'`)
	if nodeSetup < 0 || harnessRun < 0 || nodeSetup > harnessRun {
		t.Fatal("Kind workflow does not set up pinned Node before running the harness")
	}
}

func between(t *testing.T, body, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(body, startMarker)
	if start < 0 {
		t.Fatalf("missing start marker %q", startMarker)
	}
	start += len(startMarker)
	end := strings.Index(body[start:], endMarker)
	if end < 0 {
		t.Fatalf("missing end marker %q", endMarker)
	}
	return body[start : start+end]
}

type scriptResult struct {
	output string
	err    error
}

func runKindScript(t *testing.T, path string) scriptResult {
	t.Helper()
	return runKindScriptWithEnv(t, path)
}

func runKindScriptWithEnv(t *testing.T, path string, extra ...string) scriptResult {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", filepath.Join(root, "bin", "kind-e2e.sh"), "--preflight")
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + path}, extra...)...)
	out, runErr := cmd.CombinedOutput()
	return scriptResult{output: string(out), err: runErr}
}

func runKindScriptFullWithEnv(t *testing.T, path string, extra ...string) scriptResult {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", filepath.Join(root, "bin", "kind-e2e.sh"))
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + path}, extra...)...)
	out, runErr := cmd.CombinedOutput()
	return scriptResult{output: string(out), err: runErr}
}

func linkTool(t *testing.T, dir, name string) {
	t.Helper()
	source, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	if err := os.Symlink(source, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func writeStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}
