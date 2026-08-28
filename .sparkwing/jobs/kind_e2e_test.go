package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
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

func TestKindE2EExistingClusterPreflightIsExplicitAndDockerFree(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls")
	writeStub(t, bin, "kubectl", `printf 'kubectl %s\n' "$*" >>"$CALL_RECORD"`)
	writeStub(t, bin, "helm", `printf 'helm %s\n' "$*" >>"$CALL_RECORD"`)
	for _, name := range []string{"curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKindScriptWithEnv(t, bin,
		"CALL_RECORD="+record,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err != nil {
		t.Fatalf("existing-cluster preflight: %v\n%s", result.err, result.output)
	}
	body, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(body)
	for _, want := range []string{
		"kubectl version --client",
		"helm version --short",
		"kubectl config get-contexts remote-e2e",
		"kubectl --context remote-e2e version --request-timeout=10s",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("existing preflight calls missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "docker") || strings.Contains(calls, "kind") {
		t.Fatalf("existing-cluster preflight touched local infrastructure:\n%s", calls)
	}
}

func TestKindE2EExistingClusterPreflightRequiresExactCleanupAllowList(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"kubectl", "helm", "curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKindScriptWithEnv(t, bin,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=wrong/release",
	)
	if result.err == nil || !strings.Contains(result.output, "must equal sparkwing-e2e/sparkwing") {
		t.Fatalf("mismatched cleanup allow-list result = %v, %q", result.err, result.output)
	}
}

func TestKindE2EExistingClusterPreflightRejectsAnUnsafeImagePrefix(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"kubectl", "helm", "curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKindScriptWithEnv(t, bin,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing\nsecurityContext:",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil || !strings.Contains(result.output, "unsafe in an image repository") {
		t.Fatalf("unsafe image prefix result = %v, %q", result.err, result.output)
	}
}

const kindE2ETestOwner = "0123456789abcdef0123456789abcdef"

func TestKindE2EExistingClusterCleanupBeforeHelmDeletesOnlyOwnedObjects(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kindE2ETestOwner,
		"FAIL_AT=fixture",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("forced fixture rollout failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	lookup := strings.Index(got, "get namespace sparkwing-e2e --ignore-not-found -o name")
	atomicCreate := strings.Index(got, "kubectl --context remote-e2e create -f -")
	ownerCheck := strings.Index(got, "get namespace sparkwing-e2e -o jsonpath={.metadata.labels.sparkwing\\.dev/e2e-owned}")
	ownedDelete := strings.Index(got, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kindE2ETestOwner)
	if lookup < 0 || atomicCreate < lookup || ownerCheck < atomicCreate || ownedDelete < ownerCheck {
		t.Fatalf("existing cleanup did not atomically claim and verify its namespace before deleting owned objects:\n%s", got)
	}
	if strings.Contains(got, " helm --kube-context remote-e2e uninstall ") ||
		strings.Contains(got, "helm --kube-context remote-e2e list --all") ||
		strings.Contains(got, "label persistentvolumeclaim") {
		t.Fatalf("pre-Helm failure inspected, labeled, or uninstalled release resources it never attempted:\n%s", got)
	}
	for _, forbidden := range []string{"delete namespace", "delete cluster", "docker ", "kind "} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("existing cleanup touched cluster infrastructure via %q:\n%s", forbidden, got)
		}
	}
}

func TestKindE2EExistingClusterCleanupRefusesANamespaceOwnedByAnotherRun(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN=ffffffffffffffffffffffffffffffff",
		"FAIL_AT=fixture",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil || !strings.Contains(result.output, "namespace sparkwing-e2e is not owned by this run") {
		t.Fatalf("unowned cleanup result = %v, %q", result.err, result.output)
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if strings.Contains(got, " uninstall ") || strings.Contains(got, " delete ") ||
		strings.Contains(got, "label persistentvolumeclaim") {
		t.Fatalf("unowned namespace cleanup issued a destructive call:\n%s", got)
	}
}

func TestKindE2EExistingClusterLookupErrorStopsBeforeNamespaceCreate(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_LOOKUP_ERROR=1",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil || !strings.Contains(result.output, "failed to check whether namespace 'sparkwing-e2e' exists") {
		t.Fatalf("namespace lookup error result = %v, %q", result.err, result.output)
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); strings.Contains(got, " create namespace ") || strings.Contains(got, " create -f -") {
		t.Fatalf("namespace lookup error proceeded to create:\n%s", got)
	}
}

func TestKindE2EExistingClusterCreateConflictDoesNotReadOrMutateTheWinner(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"FAIL_AT=namespace-create",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("namespace create conflict unexpectedly passed")
	}
	if strings.Contains(result.output, "collecting failure diagnostics") {
		t.Fatalf("namespace create conflict collected a foreign namespace's diagnostics: %q", result.output)
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "kubectl --context remote-e2e create -f -") {
		t.Fatalf("namespace create conflict never reached the atomic create:\n%s", got)
	}
	for _, forbidden := range []string{
		"get namespace sparkwing-e2e -o jsonpath=",
		"helm --kube-context",
		" delete ",
		" logs ",
		"cluster-info dump",
	} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("namespace create conflict crossed the foreign-namespace boundary via %q:\n%s", forbidden, got)
		}
	}
}

func TestKindE2EExistingClusterUninstallsOnlyItsAttemptedRelease(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kindE2ETestOwner,
		"FAIL_AT=helm-install",
		"HELM_RELEASE=sparkwing",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("forced Helm install failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	attempt := strings.Index(got, "install sparkwing ")
	attemptOwner := strings.Index(got, "--labels sparkwing.dev/e2e-owner="+kindE2ETestOwner)
	ownedList := strings.Index(got, "list --all --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kindE2ETestOwner)
	labelReleasePVCs := strings.Index(got, "label persistentvolumeclaim -l app.kubernetes.io/instance=sparkwing")
	labelPoolPVCs := strings.Index(got, "label persistentvolumeclaim -l app=sparkwing-cache-pool,sparkwing.dev/managed=pool-manager,sparkwing.dev/pool=cache")
	uninstall := strings.Index(got, "uninstall sparkwing --namespace sparkwing-e2e")
	ownedDelete := strings.Index(got, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kindE2ETestOwner)
	if attempt < 0 || attemptOwner < attempt || ownedList < attemptOwner || labelReleasePVCs < ownedList ||
		labelPoolPVCs < labelReleasePVCs || uninstall < labelPoolPVCs || ownedDelete < uninstall {
		t.Fatalf("attempted release was not ownership-proved before labeling PVCs and uninstalling:\n%s", got)
	}
}

func TestKindE2EExistingClusterRetainsAReleaseWithoutItsOwnerLabel(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kindE2ETestOwner,
		"FAIL_AT=helm-install",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("forced Helm install failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "list --all --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kindE2ETestOwner) {
		t.Fatalf("failed install did not query durable per-run Helm metadata:\n%s", got)
	}
	if strings.Contains(got, "label persistentvolumeclaim") ||
		strings.Contains(got, " uninstall sparkwing ") {
		t.Fatalf("release without the per-run owner label was labeled or uninstalled:\n%s", got)
	}
}

func TestKindE2EExistingClusterReprovesSuccessfulReleaseBeforeCleanup(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kindE2ETestOwner,
		"FAIL_AT=post-install",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("post-install failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	ownedList := strings.Index(got, "list --all --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kindE2ETestOwner)
	if ownedList < 0 {
		t.Fatalf("cleanup trusted stale successful-install ownership:\n%s", got)
	}
	afterProof := got[ownedList:]
	for _, forbidden := range []string{"label persistentvolumeclaim", " uninstall ", " delete "} {
		if strings.Contains(afterProof, forbidden) {
			t.Fatalf("cleanup crossed failed current-release ownership proof via %q:\n%s", forbidden, afterProof)
		}
	}
	if !strings.Contains(result.output, "cleanup failed; retained namespace: sparkwing-e2e") {
		t.Fatalf("cleanup output = %q, want retained namespace", result.output)
	}
}

func TestKindE2EExistingClusterRetainsReleaseWhenPVCAdoptionIsIncomplete(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKindScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kindE2ETestOwner,
		"FAIL_AT=cleanup-release-pvc-label",
		"HELM_RELEASE=sparkwing",
		"SPARKWING_KIND_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_KIND_E2E_PROVISION=existing",
		"SPARKWING_KIND_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_KIND_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_KIND_E2E_TAG=commit-0123456789ab",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("incomplete PVC adoption unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	ownedList := strings.Index(got, "list --all --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kindE2ETestOwner)
	if ownedList < 0 {
		t.Fatalf("cleanup did not prove current release ownership:\n%s", got)
	}
	afterProof := got[ownedList:]
	for _, required := range []string{
		"label persistentvolumeclaim -l app.kubernetes.io/instance=sparkwing",
		"label persistentvolumeclaim -l app=sparkwing-cache-pool,sparkwing.dev/managed=pool-manager,sparkwing.dev/pool=cache",
	} {
		if !strings.Contains(afterProof, required) {
			t.Fatalf("cleanup did not attempt PVC adoption via %q:\n%s", required, afterProof)
		}
	}
	for _, forbidden := range []string{" uninstall ", " delete "} {
		if strings.Contains(afterProof, forbidden) {
			t.Fatalf("cleanup crossed incomplete PVC adoption via %q:\n%s", forbidden, afterProof)
		}
	}
	if !strings.Contains(result.output, "cleanup failed; retained namespace: sparkwing-e2e") {
		t.Fatalf("cleanup output = %q, want retained namespace", result.output)
	}
}

func existingClusterFailureHarness(t *testing.T) (bin, calls, artifacts string) {
	t.Helper()
	bin = t.TempDir()
	for _, name := range []string{"cat", "dirname", "grep", "jq", "mkdir"} {
		linkTool(t, bin, name)
	}
	calls = filepath.Join(t.TempDir(), "calls")
	writeStub(t, bin, "kubectl", `
printf 'kubectl %s\n' "$*" >>"$CALL_RECORD"
case "$*" in
  *"get namespace sparkwing-e2e --ignore-not-found -o name"*)
    if [ "${NAMESPACE_LOOKUP_ERROR:-0}" = "1" ]; then
      exit 42
    fi
    ;;
  *"get namespace sparkwing-e2e -o jsonpath="*)
    printf '%s\t%s' "$NAMESPACE_OWNER" "$NAMESPACE_OWNER_TOKEN"
    ;;
  *"--context remote-e2e create -f -"*)
    if [ "${FAIL_AT:-}" = "namespace-create" ]; then
      exit 23
    fi
    ;;
  *"rollout status deployment/kind-repo"*)
    if [ "${FAIL_AT:-}" = "fixture" ]; then
      exit 23
    fi
    ;;
  *"label persistentvolumeclaim -l app.kubernetes.io/instance=sparkwing"*)
    if [ "${FAIL_AT:-}" = "post-install" ] || [ "${FAIL_AT:-}" = "cleanup-release-pvc-label" ]; then
      exit 23
    fi
    ;;
esac
`)
	writeStub(t, bin, "helm", `
printf 'helm %s\n' "$*" >>"$CALL_RECORD"
case "$*" in
  *" install sparkwing "*)
    if [ "${FAIL_AT:-}" = "helm-install" ] || [ "${FAIL_AT:-}" = "cleanup-release-pvc-label" ]; then
      exit 23
    fi
    ;;
  *"list --all --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="*)
    if [ -n "${HELM_RELEASE:-}" ]; then
      printf '[{"name":"%s"}]\n' "$HELM_RELEASE"
    else
      printf '[]\n'
    fi
    ;;
esac
`)
	writeStub(t, bin, "curl", "exit 0\n")
	writeStub(t, bin, "openssl", "printf '"+kindE2ETestOwner+"\\n'\n")
	artifacts = filepath.Join(t.TempDir(), "artifacts")
	return bin, calls, artifacts
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
	for _, name := range []string{"curl", "jq"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	writeStub(t, bin, "openssl", "printf '"+kindE2ETestOwner+"\\n'\n")
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
		"git ls-remote git://127.0.0.1/smoke.git",
		`git config --global --add url."git://kind-repo.${namespace}.svc.cluster.local/".insteadOf`,
		`"https://github.com/sparkwing-kind/"`,
		`"git@github.com:sparkwing-kind/"`,
		"kind load docker-image",
		"helm_e2e install",
		"SPARKWING_KIND_E2E_PROVISION",
		"SPARKWING_KIND_E2E_ALLOW_CLEANUP",
		"create namespace \"$namespace\" --dry-run=client -o yaml",
		"sparkwing.dev/e2e-owner=$run_owner",
		"--description \"$release_owner_description\"",
		"--labels \"$owner_token_label\"",
		"provision_mode=$provision_mode",
		"kube_context=$kube_context",
		"namespace=$namespace",
		"release=$release_name",
		"image_prefix=$image_prefix",
		"image_tag=$image_tag",
		"configmap sparkwing-kind-fixture",
		"initContainers:",
		"readinessProbe:",
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
		"helm_e2e get manifest",
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
	if strings.Contains(script, "command -v git-daemon") {
		t.Fatal("Kind harness requires git-daemon as a standalone PATH binary")
	}
	if strings.Contains(script, "hostPath:") || strings.Contains(script, "extraMounts:") {
		t.Fatal("Kubernetes fixture depends on a Kind-only host mount")
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

func TestKindE2EWaitRunStatusRetriesOnlyNotFound(t *testing.T) {
	result, calls := runWaitRunStatus(t,
		"{\"message\":\"not found\"}\n404",
		"{\"status\":\"success\"}\n200",
	)
	if result.err != nil {
		t.Fatalf("transient run lookup: %v\n%s", result.err, result.output)
	}
	if calls != 2 {
		t.Fatalf("run lookup calls = %d, want 2", calls)
	}
}

func TestKindE2EWaitRunStatusRejectsOtherHTTPFailures(t *testing.T) {
	result, calls := runWaitRunStatus(t, "{\"message\":\"unavailable\"}\n500")
	if result.err == nil {
		t.Fatal("run lookup accepted HTTP 500")
	}
	if calls != 1 {
		t.Fatalf("run lookup calls = %d, want 1", calls)
	}
	if !strings.Contains(result.output, "run run-1 returned HTTP 500 while waiting for success") {
		t.Fatalf("run lookup output = %q, want HTTP 500 failure", result.output)
	}
}

func runWaitRunStatus(t *testing.T, responses ...string) (scriptResult, int) {
	t.Helper()
	script := readHostedCIFile(t, "bin/kind-e2e.sh")
	waitFunction := "wait_run_status() {\n" + between(t, script,
		"wait_run_status() {\n",
		"\n}\n\necho \"kind-e2e: proving invalid webhook authentication\"",
	) + "\n}\n"
	responseDir := t.TempDir()
	for i, response := range responses {
		if err := os.WriteFile(filepath.Join(responseDir, strconv.Itoa(i)), []byte(response), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	countFile := filepath.Join(t.TempDir(), "count")
	if err := os.WriteFile(countFile, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	harness := `set -Eeuo pipefail
next_response() {
  local index response_path
  index="$(<"$COUNT_FILE")"
  response_path="$RESPONSES_DIR/$index"
  printf '%s' "$((index + 1))" >"$COUNT_FILE"
  if [[ ! -f "$response_path" ]]; then
    printf '{}\n599'
    return
  fi
  printf '%s' "$(<"$response_path")"
}
api_get() {
  local response status
  response="$(next_response)"
  status="${response##*$'\n'}"
  [[ "$status" == "200" ]] || return 22
  printf '%s' "${response%$'\n'*}"
}
api_get_with_status() {
  next_response
}
jq() {
  local input= line
  while IFS= read -r line; do
    input+="$line"
  done
  input="${input#*\"status\":\"}"
  printf '%s\n' "${input%%\"*}"
}
sleep() {
  :
}
die() {
  printf 'kind-e2e: %s\n' "$*" >&2
  exit 1
}
` + waitFunction + `
wait_run_status run-1 success 5
`
	cmd := exec.Command("/bin/bash", "-c", harness)
	cmd.Env = append(os.Environ(), "COUNT_FILE="+countFile, "RESPONSES_DIR="+responseDir)
	out, runErr := cmd.CombinedOutput()
	countBody, err := os.ReadFile(countFile)
	if err != nil {
		t.Fatal(err)
	}
	calls, err := strconv.Atoi(string(countBody))
	if err != nil {
		t.Fatal(err)
	}
	return scriptResult{output: string(out), err: runErr}, calls
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
		`kubectl_url="https://dl.k8s.io/release/v1.36.1/bin/linux/amd64/kubectl"`,
		`kubectl_sha256="629d3f410e09bf49b64ae7079f7f0bda1191efed311f7d37fdbab0ad5b0ec2b7"`,
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
	if strings.Contains(body, "go install k8s.io/kubectl") {
		t.Fatal("Kind workflow uses the kubectl module as an installable package")
	}
	kubectlDownload := strings.Index(body, `curl --fail --location --silent --show-error --output "$kubectl_path" "$kubectl_url"`)
	kubectlVerify := strings.Index(body, `printf '%s  %s\n' "$kubectl_sha256" "$kubectl_path" | sha256sum --check --strict`)
	kubectlInstall := strings.Index(body, `install -m 0755 "$kubectl_path" "$GOBIN/kubectl"`)
	if kubectlDownload < 0 || kubectlVerify < kubectlDownload || kubectlInstall < kubectlVerify {
		t.Fatal("Kind workflow does not verify the pinned kubectl artifact before installing it")
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
