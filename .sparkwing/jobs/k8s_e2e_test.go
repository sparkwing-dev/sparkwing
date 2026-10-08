package jobs

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	uninstall := strings.Index(got, "uninstall sparkwing --namespace sparkwing-e2e")
	ownedDelete := strings.Index(got, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	if attempt < 0 || attemptOwner < attempt || ownedList < attemptOwner || labelReleasePVCs < ownedList ||
		uninstall < labelReleasePVCs || ownedDelete < uninstall {
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
	uninstall := strings.Index(afterProof, "uninstall sparkwing --namespace sparkwing-e2e")
	ownedDelete := strings.Index(afterProof, "delete deployment,service,configmap,secret,persistentvolumeclaim -l sparkwing.dev/e2e-owned=true,sparkwing.dev/e2e-owner="+kubernetesE2ETestOwner)
	if labelReleasePVCs < 0 || uninstall < labelReleasePVCs || ownedDelete < uninstall {
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
	if !strings.Contains(afterProof, "label persistentvolumeclaim -l app.kubernetes.io/instance=sparkwing") {
		t.Fatalf("cleanup did not attempt PVC adoption:\n%s", afterProof)
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
		"invalid trigger token returned $invalid_trigger_status, want 401",
		".runs | length == 0",
		"/api/v1/triggers",
		"/api/v1/tokens",
		"/api/v1/agents",
		"/logs/prove-controller-runner-logs",
		"/cancel",
		"/retry?full=1",
		"rollout restart \"deployment/$runner_deployment\"",
		`prove_runner_execution "$success_run" "$initial_runner_pod" initial`,
		`prove_runner_execution "$post_runner_restart_run" "$runner_pod_after" post-restart`,
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
	invalidTrigger := strings.Index(script, "k8s-e2e: proving invalid trigger authentication")
	validTrigger := strings.Index(script, "submit_trigger k8s-success")
	if invalidTrigger < 0 || validTrigger < 0 || invalidTrigger > validTrigger {
		t.Fatal("Kubernetes harness does not reject an invalid token before its first valid trigger")
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

const kubernetesRunnerExecutionNodes = `{"nodes":[{"id":"prove-controller-runner-logs","status":"done","execution_attempts":[{"outcome":"success","executor_kind":"kubernetes","execution_site":"cluster","execution_site_name":"runner-new"}]}]}`

func TestKubernetesE2EProvesSuccessfulExecutionByTheExpectedReadyRunnerPod(t *testing.T) {
	result := runProveRunnerExecution(t, kubernetesRunnerExecutionNodes)
	if result.err != nil {
		t.Fatalf("runner execution proof: %v\n%s", result.err, result.output)
	}
}

func TestKubernetesE2ERejectsUnprovenRunnerExecution(t *testing.T) {
	for _, tc := range []struct{ name, nodes string }{
		{"wrong pod", strings.Replace(kubernetesRunnerExecutionNodes, `"runner-new"`, `"runner-old"`, 1)},
		{"failed attempt", strings.Replace(kubernetesRunnerExecutionNodes, `"success"`, `"failed"`, 1)},
		{"missing attempts", `{"nodes":[{"id":"prove-controller-runner-logs","status":"done"}]}`},
		{"wrong node", strings.Replace(kubernetesRunnerExecutionNodes, `"prove-controller-runner-logs"`, `"other-node"`, 1)},
		{"wrong executor", strings.Replace(kubernetesRunnerExecutionNodes, `"kubernetes"`, `"local"`, 1)},
		{"wrong site", strings.Replace(kubernetesRunnerExecutionNodes, `"cluster"`, `"local"`, 1)},
		{"unfinished node", strings.Replace(kubernetesRunnerExecutionNodes, `"done"`, `"running"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runProveRunnerExecution(t, tc.nodes)
			if result.err == nil {
				t.Fatal("runner execution proof accepted unproven execution")
			}
			if !strings.Contains(result.output, "test run run-1 has no successful execution by Ready runner pod runner-new") {
				t.Fatalf("runner execution proof output = %q", result.output)
			}
		})
	}
}

func runProveRunnerExecution(t *testing.T, nodes string) scriptResult {
	t.Helper()
	script := readHostedCIFile(t, "bin/k8s-e2e.sh")
	proveFunction := "prove_runner_execution() {\n" + between(t, script,
		"prove_runner_execution() {\n",
		"\n}\n\nwait_run_status() {",
	) + "\n}\n"
	artifactDir := t.TempDir()
	callsFile := filepath.Join(t.TempDir(), "calls")
	harness := `set -Eeuo pipefail
api_get() {
  printf 'get %s\n' "$1" >>"$CALLS_FILE"
  printf '%s' "$NODES_JSON"
}
die() {
  printf 'k8s-e2e: %s\n' "$*" >&2
  exit 1
}
` + proveFunction + `
prove_runner_execution run-1 runner-new test
`
	cmd := exec.Command(kubernetesTestBash(t), "-c", harness)
	cmd.Env = kubernetesTestEnv(t,
		"CALLS_FILE="+callsFile,
		"NODES_JSON="+nodes,
		"artifact_dir="+artifactDir,
	)
	out, runErr := cmd.CombinedOutput()
	calls, err := os.ReadFile(callsFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "get /api/v1/runs/run-1/nodes\n" {
		t.Fatalf("runner execution proof calls = %q", calls)
	}
	evidence, err := os.ReadFile(filepath.Join(artifactDir, "runner-test-nodes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(evidence) != nodes+"\n" {
		t.Fatalf("runner execution evidence = %q, want %q", evidence, nodes+"\n")
	}
	return scriptResult{output: string(out), err: runErr}
}

func runWaitRunStatus(t *testing.T, responses ...string) (scriptResult, int) {
	t.Helper()
	script := readHostedCIFile(t, "bin/k8s-e2e.sh")
	waitFunction := "wait_run_status() {\n" + between(t, script,
		"wait_run_status() {\n",
		"\n}\n\necho \"k8s-e2e: proving invalid trigger authentication\"",
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
	cmd := exec.Command(kubernetesTestBash(t), "-c", harness)
	cmd.Env = kubernetesTestEnv(t, "COUNT_FILE="+countFile, "RESPONSES_DIR="+responseDir)
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
	cmd := exec.Command(kubernetesTestBash(t), filepath.ToSlash(filepath.Join(root, "bin", "k8s-e2e.sh")), "--preflight")
	cmd.Env = kubernetesTestEnv(t, append([]string{"PATH=" + path}, extra...)...)
	out, runErr := cmd.CombinedOutput()
	return scriptResult{output: string(out), err: runErr}
}

func runKubernetesScriptFullWithEnv(t *testing.T, path string, extra ...string) scriptResult {
	t.Helper()
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(kubernetesTestBash(t), filepath.ToSlash(filepath.Join(root, "bin", "k8s-e2e.sh")))
	cmd.Env = kubernetesTestEnv(t, append([]string{"PATH=" + path}, extra...)...)
	out, runErr := cmd.CombinedOutput()
	return scriptResult{output: string(out), err: runErr}
}

func linkTool(t *testing.T, dir, name string) {
	t.Helper()
	source, err := exec.LookPath(name)
	if runtime.GOOS == "windows" {
		matching := filepath.Join(filepath.Dir(kubernetesTestBash(t)), name+".exe")
		if info, statErr := os.Stat(matching); statErr == nil && info.Mode().IsRegular() {
			source, err = matching, nil
		}
	}
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

func kubernetesTestBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return "/bin/bash"
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	direct := filepath.Join(filepath.Dir(filepath.Dir(shell)), "usr", "bin", "bash.exe")
	if info, err := os.Stat(direct); err == nil && info.Mode().IsRegular() {
		return direct
	}
	return shell
}

func kubernetesTestEnv(t *testing.T, extra ...string) []string {
	env := append([]string{}, os.Environ()...)
	for _, entry := range extra {
		name, value, ok := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" && ok && (strings.HasSuffix(name, "_FILE") || strings.HasSuffix(name, "_DIR") || name == "CALL_RECORD" || name == "artifact_dir") {
			entry = name + "=" + filepath.ToSlash(value)
		}
		if runtime.GOOS == "windows" && name == "PATH" {
			// hack: copied Windows POSIX tools need their runtime DLL directory.
			entry += string(os.PathListSeparator) + filepath.Dir(kubernetesTestBash(t))
		}
		env = append(env, entry)
	}
	return env
}
