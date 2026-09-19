package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestKubernetesE2EPipelineIsRegisteredAndBounded(t *testing.T) {
	registration, ok := sparkwing.Lookup("k8s-e2e")
	if !ok {
		t.Fatal("k8s-e2e is not registered")
	}
	plan, err := registration.Invoke(context.Background(), nil, sparkwing.RunContext{Pipeline: "k8s-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	nodes := plan.Nodes()
	if len(nodes) != 1 || nodes[0].ID() != "k8s-e2e" {
		t.Fatalf("k8s-e2e nodes = %v, want [k8s-e2e]", nodeIDs(nodes))
	}
	if got := nodes[0].TimeoutDuration(); got != 40*time.Minute {
		t.Fatalf("k8s-e2e timeout = %s, want 40m", got)
	}
}

func TestKubernetesE2ECommandCancellationRunsCleanup(t *testing.T) {
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
	configureKubernetesE2ECommand(cmd)
	if cmd.Cancel == nil || cmd.WaitDelay != 90*time.Second {
		t.Fatal("Kubernetes command cancellation is not bounded and graceful")
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
			t.Fatal("Kubernetes command did not become ready for cancellation")
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
		t.Fatal("Kubernetes command did not honor cancellation")
	}
	if got, err := os.ReadFile(cleanupMarker); err != nil || string(got) != "cleanup" {
		t.Fatalf("Kubernetes cancellation cleanup marker = %q, %v", got, err)
	}
}

func TestKubernetesE2EPreflightIsExplicitAndLocalInfrastructureFree(t *testing.T) {
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls")
	writeStub(t, bin, "kubectl", `printf 'kubectl %s\n' "$*" >>"$CALL_RECORD"`)
	writeStub(t, bin, "helm", `printf 'helm %s\n' "$*" >>"$CALL_RECORD"`)
	for _, name := range []string{"curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKubernetesScriptWithEnv(t, bin,
		"CALL_RECORD="+record,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err != nil {
		t.Fatalf("Kubernetes preflight: %v\n%s", result.err, result.output)
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
			t.Errorf("Kubernetes preflight calls missing %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "docker") || strings.Contains(calls, "kind") {
		t.Fatalf("Kubernetes preflight touched local infrastructure:\n%s", calls)
	}
}

func TestKubernetesE2EExistingClusterPreflightRequiresExactCleanupAllowList(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"kubectl", "helm", "curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKubernetesScriptWithEnv(t, bin,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=wrong/release",
	)
	if result.err == nil || !strings.Contains(result.output, "must equal sparkwing-e2e/sparkwing") {
		t.Fatalf("mismatched cleanup allow-list result = %v, %q", result.err, result.output)
	}
}

func TestKubernetesE2EExistingClusterPreflightRejectsAnUnsafeImagePrefix(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"kubectl", "helm", "curl", "jq", "openssl"} {
		writeStub(t, bin, name, "exit 0\n")
	}
	result := runKubernetesScriptWithEnv(t, bin,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing\nsecurityContext:",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil || !strings.Contains(result.output, "unsafe in an image repository") {
		t.Fatalf("unsafe image prefix result = %v, %q", result.err, result.output)
	}
}

const kubernetesE2ETestOwner = "0123456789abcdef0123456789abcdef"

func TestKubernetesE2EExistingClusterCleanupBeforeHelmDeletesOnlyOwnedObjects(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kubernetesE2ETestOwner,
		"FAIL_AT=fixture",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
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
	ownedDelete := strings.Index(got, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	if lookup < 0 || atomicCreate < lookup || ownerCheck < atomicCreate || ownedDelete < ownerCheck {
		t.Fatalf("existing cleanup did not atomically claim and verify its namespace before deleting owned objects:\n%s", got)
	}
	if strings.Contains(got, " helm --kube-context remote-e2e uninstall ") ||
		strings.Contains(got, "helm --kube-context remote-e2e list --namespace") ||
		strings.Contains(got, "label persistentvolumeclaim") {
		t.Fatalf("pre-Helm failure inspected, labeled, or uninstalled release resources it never attempted:\n%s", got)
	}
	for _, forbidden := range []string{"delete namespace", "delete cluster", "docker ", "kind "} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("existing cleanup touched cluster infrastructure via %q:\n%s", forbidden, got)
		}
	}
}

func TestKubernetesE2EExistingClusterCleanupRefusesANamespaceOwnedByAnotherRun(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN=ffffffffffffffffffffffffffffffff",
		"FAIL_AT=fixture",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
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

func TestKubernetesE2EExistingClusterLookupErrorStopsBeforeNamespaceCreate(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_LOOKUP_ERROR=1",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
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

func TestKubernetesE2EExistingClusterCreateConflictDoesNotReadOrMutateTheWinner(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"FAIL_AT=namespace-create",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
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

func TestKubernetesE2EExistingClusterUninstallsOnlyItsAttemptedRelease(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kubernetesE2ETestOwner,
		"FAIL_AT=helm-install",
		"HELM_RELEASE=sparkwing",
		"HELM_RELEASE_STATUS=failed",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
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
	attemptOwner := strings.Index(got, "--labels sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	ownedList := strings.Index(got, "list --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	labelReleasePVCs := strings.Index(got, "label persistentvolumeclaim -l app.kubernetes.io/instance=sparkwing")
	labelPoolPVCs := strings.Index(got, "label persistentvolumeclaim -l app=sparkwing-cache-pool,sparkwing.dev/managed=pool-manager,sparkwing.dev/pool=cache")
	uninstall := strings.Index(got, "uninstall sparkwing --namespace sparkwing-e2e")
	ownedDelete := strings.Index(got, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	if attempt < 0 || attemptOwner < attempt || ownedList < attemptOwner || labelReleasePVCs < ownedList ||
		labelPoolPVCs < labelReleasePVCs || uninstall < labelPoolPVCs || ownedDelete < uninstall {
		t.Fatalf("attempted release was not ownership-proved before labeling PVCs and uninstalling:\n%s", got)
	}
}

func TestKubernetesE2EExistingClusterReprovesDeployedReleaseBeforeCleanup(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kubernetesE2ETestOwner,
		"FAIL_AT=post-install-resource",
		"HELM_RELEASE=sparkwing",
		"HELM_RELEASE_STATUS=deployed",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("post-install resource failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	ownedList := strings.Index(got, "list --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	if ownedList < 0 {
		t.Fatalf("cleanup did not query deployed release ownership:\n%s", got)
	}
	afterProof := got[ownedList:]
	labelReleasePVCs := strings.Index(afterProof, "label persistentvolumeclaim -l app.kubernetes.io/instance=sparkwing")
	labelPoolPVCs := strings.Index(afterProof, "label persistentvolumeclaim -l app=sparkwing-cache-pool,sparkwing.dev/managed=pool-manager,sparkwing.dev/pool=cache")
	uninstall := strings.Index(afterProof, "uninstall sparkwing --namespace sparkwing-e2e")
	ownedDelete := strings.Index(afterProof, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	if labelReleasePVCs < 0 || labelPoolPVCs < labelReleasePVCs || uninstall < labelPoolPVCs || ownedDelete < uninstall {
		t.Fatalf("deployed release ownership did not authorize cleanup in order:\n%s", afterProof)
	}
}

func TestKubernetesE2EExistingClusterRetainsAReleaseWithoutItsOwnerLabel(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kubernetesE2ETestOwner,
		"FAIL_AT=helm-install",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("forced Helm install failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	if !strings.Contains(got, "list --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner) {
		t.Fatalf("failed install did not query durable per-run Helm metadata:\n%s", got)
	}
	if strings.Contains(got, "label persistentvolumeclaim") ||
		strings.Contains(got, " uninstall sparkwing ") {
		t.Fatalf("release without the per-run owner label was labeled or uninstalled:\n%s", got)
	}
}

func TestKubernetesE2EExistingClusterReprovesSuccessfulReleaseBeforeCleanup(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kubernetesE2ETestOwner,
		"FAIL_AT=post-install",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("post-install failure unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	ownedList := strings.Index(got, "list --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
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

func TestKubernetesE2EExistingClusterRetainsReleaseWhenPVCAdoptionIsIncomplete(t *testing.T) {
	bin, calls, artifacts := existingClusterFailureHarness(t)
	result := runKubernetesScriptFullWithEnv(t, bin,
		"CALL_RECORD="+calls,
		"NAMESPACE_OWNER=true",
		"NAMESPACE_OWNER_TOKEN="+kubernetesE2ETestOwner,
		"FAIL_AT=cleanup-release-pvc-label",
		"HELM_RELEASE=sparkwing",
		"HELM_RELEASE_STATUS=failed",
		"SPARKWING_K8S_E2E_ARTIFACT_DIR="+artifacts,
		"SPARKWING_K8S_E2E_KUBE_CONTEXT=remote-e2e",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX=registry.example/sparkwing",
		"SPARKWING_K8S_E2E_TAG=commit-0123456789ab",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing",
	)
	if result.err == nil {
		t.Fatal("incomplete PVC adoption unexpectedly passed")
	}
	body, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	ownedList := strings.Index(got, "list --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
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
  *"rollout status deployment/k8s-repo"*)
    if [ "${FAIL_AT:-}" = "fixture" ]; then
      exit 23
    fi
    ;;
  *"get deployment -l app.kubernetes.io/instance=sparkwing,app.kubernetes.io/component=controller"*)
    if [ "${FAIL_AT:-}" = "post-install-resource" ]; then
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
  *" list --all "*)
    exit 64
    ;;
  *" install sparkwing "*)
    if [ "${FAIL_AT:-}" = "helm-install" ] || [ "${FAIL_AT:-}" = "cleanup-release-pvc-label" ]; then
      exit 23
    fi
    ;;
  *"list --namespace sparkwing-e2e --selector sparkwing.dev/e2e-owner="*)
    if [ -n "${HELM_RELEASE:-}" ]; then
      printf '[{"name":"%s","status":"%s"}]\n' "$HELM_RELEASE" "$HELM_RELEASE_STATUS"
    else
      printf '[]\n'
    fi
    ;;
esac
`)
	writeStub(t, bin, "curl", "exit 0\n")
	writeStub(t, bin, "openssl", "printf '"+kubernetesE2ETestOwner+"\\n'\n")
	artifacts = filepath.Join(t.TempDir(), "artifacts")
	return bin, calls, artifacts
}

func TestKubernetesE2EUsesExplicitReleaseImagesAndCapturesFailureEvidence(t *testing.T) {
	script := readHostedCIFile(t, "bin/k8s-e2e.sh")
	for _, component := range buildImagesComponents {
		if !strings.Contains(script, "repository: ${image_prefix}"+component.name) &&
			!strings.Contains(script, "$(image_ref "+component.name+")") {
			t.Errorf("Kubernetes harness is missing release image %q", component.name)
		}
	}
	for _, marker := range []string{
		`git config --global --add url."git://k8s-repo.${namespace}.svc.cluster.local/".insteadOf`,
		`"https://github.com/sparkwing-k8s/"`,
		`"git@github.com:sparkwing-k8s/"`,
		"helm_e2e install",
		"SPARKWING_K8S_E2E_KUBE_CONTEXT",
		"SPARKWING_K8S_E2E_IMAGE_PREFIX",
		"SPARKWING_K8S_E2E_TAG",
		"SPARKWING_K8S_E2E_ALLOW_CLEANUP",
		"create namespace \"$namespace\" --dry-run=client -o yaml",
		"sparkwing.dev/e2e-owner=$run_owner",
		"--description \"$release_owner_description\"",
		"--labels \"$owner_token_label\"",
		"kube_context=$kube_context",
		"namespace=$namespace",
		"release=$release_name",
		"image_prefix=$image_prefix",
		"image_tag=$image_tag",
		"configmap sparkwing-k8s-fixture",
		"initContainers:",
		"readinessProbe:",
		"invalid webhook returned $invalid_webhook_status, want 401",
		"sha256=0000000000000000000000000000000000000000000000000000000000000000",
		".runs | length == 0",
		"X-Hub-Signature-256",
		"/webhooks/github/${pipeline}",
		"/api/v1/tokens",
		"/api/v1/agents",
		`"/api/v1/runs/$run_id/nodes/$node_id/mark-ready"`,
		`"needs_labels":["cluster"]`,
		"/logs/prove-controller-runner-logs",
		"/cancel",
		"/retry?full=1",
		"rollout restart \"deployment/$runner_deployment\"",
		`prove_runner_claim "$initial_runner_pod" "initial runner"`,
		`prove_runner_claim "$runner_pod_after" "post-restart runner"`,
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
	} {
		if !strings.Contains(script, marker) {
			t.Errorf("Kubernetes harness is missing contract marker %q", marker)
		}
	}
	for _, forbidden := range []string{"docker ", "kind create", "kind load", "kind delete", "kind export", "kindest/node"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("Kubernetes harness contains local infrastructure command %q", forbidden)
		}
	}
	if strings.Contains(script, "command -v git-daemon") {
		t.Fatal("Kubernetes harness requires git-daemon as a standalone PATH binary")
	}
	if strings.Contains(script, "helm_e2e list --all") {
		t.Fatal("Kubernetes harness uses Helm v3's removed list --all flag")
	}
	if strings.Contains(script, "hostPath:") || strings.Contains(script, "extraMounts:") {
		t.Fatal("Kubernetes fixture depends on a node host mount")
	}
	if strings.Contains(script, `post_runner_claim" != "$success_claim`) {
		t.Fatal("runner restart proof compares per-claim nonce values instead of the replacement pod hostname")
	}
	for _, block := range []string{
		between(t, script, `success_nodes="$(api_get "/api/v1/runs/$success_run/nodes")"`, `start_forward "$web_service" 80 web`),
		between(t, script, `post_runner_nodes="$(api_get "/api/v1/runs/$post_runner_restart_run/nodes")"`, `echo "k8s-e2e: proving controller restart`),
		between(t, script, `retry_nodes="$(api_get "/api/v1/runs/$retry_run/nodes")"`, `echo "k8s-e2e: proving uninstall retention`),
	} {
		if strings.Contains(block, `claimed_by | startswith("runner:")`) {
			t.Fatal("in-process trigger execution is presented as a pool-runner claim")
		}
	}
	if strings.Contains(script, "<!DOCTYPE html") || strings.Contains(script, "logs, and dashboard") {
		t.Fatal("Kubernetes harness claims functional dashboard coverage from an HTML shell")
	}
	if got := strings.Count(script, `start_forward "$controller_service" 80`); got != 4 {
		t.Fatalf("controller forward starts = %d, want bootstrap, authenticated, restarted, and reinstalled", got)
	}
	invalidWebhook := strings.Index(script, "k8s-e2e: proving invalid webhook authentication")
	validWebhook := strings.Index(script, "send_webhook k8s-success")
	if invalidWebhook < 0 || validWebhook < 0 || invalidWebhook > validWebhook {
		t.Fatal("Kubernetes harness does not reject an invalid HMAC before its first valid webhook")
	}
	fixture := readHostedCIFile(t, "testdata/k8s-e2e/repo/.sparkwing/jobs/k8s.go")
	for _, marker := range []string{
		"sparkwing.Produces[string]",
		"runID: rc.RunID",
		"sparkwing-k8s-e2e-success run_id=%s",
		"return j.runID, nil",
	} {
		if !strings.Contains(fixture, marker) {
			t.Errorf("Kubernetes fixture is missing causal retry marker %q", marker)
		}
	}
}

func TestKubernetesE2EWaitRunStatusRetriesOnlyNotFound(t *testing.T) {
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

func TestKubernetesE2EWaitRunStatusRejectsOtherHTTPFailures(t *testing.T) {
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

func TestKubernetesE2EProvesTheExpectedRunnerPodClaimedTheProbe(t *testing.T) {
	result, calls := runProveRunnerClaim(t,
		`{"nodes":[{"claimed_by":null}]}`,
		`{"nodes":[{"claimed_by":"runner:runner-new:123"}]}`,
	)
	if result.err != nil {
		t.Fatalf("runner claim proof: %v\n%s", result.err, result.output)
	}
	for _, want := range []string{
		"post-json /api/v1/runs ",
		"post-json /api/v1/runs/k8s-runner-probe-0123456789ab-1/nodes ",
		"post /api/v1/runs/k8s-runner-probe-0123456789ab-1/nodes/runner-claim/mark-ready",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("runner probe calls missing %q:\n%s", want, calls)
		}
	}
	if !strings.Contains(result.output, "runner:runner-new:123") {
		t.Fatalf("runner claim proof output = %q", result.output)
	}
}

func TestKubernetesE2ERejectsAProbeClaimedByAnotherRunnerPod(t *testing.T) {
	result, _ := runProveRunnerClaim(t,
		`{"nodes":[{"claimed_by":"runner:runner-old:123"}]}`,
	)
	if result.err == nil {
		t.Fatal("runner claim proof accepted another runner pod")
	}
	if !strings.Contains(result.output, "does not identify Ready runner pod runner-new") {
		t.Fatalf("runner claim proof output = %q", result.output)
	}
}

func runProveRunnerClaim(t *testing.T, responses ...string) (scriptResult, string) {
	t.Helper()
	script := readHostedCIFile(t, "bin/k8s-e2e.sh")
	proveFunction := "prove_runner_claim() {\n" + between(t, script,
		"prove_runner_claim() {\n",
		"\n}\n\nwait_run_status() {",
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
	callsFile := filepath.Join(t.TempDir(), "calls")
	harness := `set -Eeuo pipefail
api_post_json() {
  printf 'post-json %s %s\n' "$1" "$2" >>"$CALLS_FILE"
}
api_post() {
  printf 'post %s\n' "$1" >>"$CALLS_FILE"
}
api_get() {
  local index response_path
  index="$(<"$COUNT_FILE")"
  response_path="$RESPONSES_DIR/$index"
  printf '%s' "$((index + 1))" >"$COUNT_FILE"
  if [[ ! -f "$response_path" ]]; then
    printf '{"nodes":[{"claimed_by":null}]}'
    return
  fi
  cat "$response_path"
}
sleep() {
  :
}
die() {
  printf 'k8s-e2e: %s\n' "$*" >&2
  exit 1
}
run_owner=0123456789abcdef0123456789abcdef
runner_probe_sequence=0
` + proveFunction + `
prove_runner_claim runner-new test
printf '%s\n%s\n' "$runner_probe_run_id" "$runner_probe_claim"
`
	cmd := exec.Command("/bin/bash", "-c", harness)
	cmd.Env = append(os.Environ(),
		"CALLS_FILE="+callsFile,
		"COUNT_FILE="+countFile,
		"RESPONSES_DIR="+responseDir,
	)
	out, runErr := cmd.CombinedOutput()
	calls, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	return scriptResult{output: string(out), err: runErr}, string(calls)
}

func runWaitRunStatus(t *testing.T, responses ...string) (scriptResult, int) {
	t.Helper()
	script := readHostedCIFile(t, "bin/k8s-e2e.sh")
	waitFunction := "wait_run_status() {\n" + between(t, script,
		"wait_run_status() {\n",
		"\n}\n\necho \"k8s-e2e: proving invalid webhook authentication\"",
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
  printf 'k8s-e2e: %s\n' "$*" >&2
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

func runKubernetesScriptWithEnv(t *testing.T, path string, extra ...string) scriptResult {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", filepath.Join(root, "bin", "k8s-e2e.sh"), "--preflight")
	cmd.Env = append(os.Environ(), append([]string{"PATH=" + path}, extra...)...)
	out, runErr := cmd.CombinedOutput()
	return scriptResult{output: string(out), err: runErr}
}

func runKubernetesScriptFullWithEnv(t *testing.T, path string, extra ...string) scriptResult {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/bash", filepath.Join(root, "bin", "k8s-e2e.sh"))
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
