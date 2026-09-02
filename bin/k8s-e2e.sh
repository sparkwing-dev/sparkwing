#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
namespace="${SPARKWING_K8S_E2E_NAMESPACE:-sparkwing-e2e}"
release_name="${SPARKWING_K8S_E2E_RELEASE:-sparkwing}"
image_tag="${SPARKWING_K8S_E2E_TAG:-}"
image_prefix="${SPARKWING_K8S_E2E_IMAGE_PREFIX:-}"
image_pull_policy="${SPARKWING_K8S_E2E_PULL_POLICY:-IfNotPresent}"
kube_context="${SPARKWING_K8S_E2E_KUBE_CONTEXT:-}"
cleanup_allow="${SPARKWING_K8S_E2E_ALLOW_CLEANUP:-}"
keep_resources="${SPARKWING_K8S_E2E_KEEP_RESOURCES:-0}"
webhook_secret="sparkwing-k8s-webhook"
ownership_label="sparkwing.dev/e2e-owned=true"
owner_token_label=""
ownership_selector=""
release_owner_description=""
artifact_dir=""
namespace_owned=0
release_installed=0
release_install_attempted=0
release_owned=0
admin_token=""
controller_port=""
web_port=""
forward_pids=()

die() {
  echo "k8s-e2e: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command '$1' is not installed"
}

kube() {
  command kubectl --context "$kube_context" "$@"
}

helm_e2e() {
  command helm --kube-context "$kube_context" "$@"
}

image_ref() {
  printf '%s%s:%s' "$image_prefix" "$1" "$image_tag"
}

create_owned_namespace() {
  kube create namespace "$namespace" --dry-run=client -o yaml | \
    kube label --local -f - "$ownership_label" "$owner_token_label" -o yaml | \
    kube create -f -
}

create_owned_secret() {
  local name=$1
  shift
  kube --namespace "$namespace" create secret generic "$name" "$@" --dry-run=client -o yaml | \
    kube --namespace "$namespace" label --local -f - "$ownership_label" "$owner_token_label" -o yaml | \
    kube --namespace "$namespace" apply -f -
}

create_owned_configmap() {
  local name=$1
  shift
  kube --namespace "$namespace" create configmap "$name" "$@" --dry-run=client -o yaml | \
    kube --namespace "$namespace" label --local -f - "$ownership_label" "$owner_token_label" -o yaml | \
    kube --namespace "$namespace" apply -f -
}

label_owned_release_pvcs() {
  local label_status=0
  kube --namespace "$namespace" label persistentvolumeclaim \
    -l "app.kubernetes.io/instance=$release_name" \
    "$ownership_label" "$owner_token_label" --overwrite || label_status=1
  kube --namespace "$namespace" label persistentvolumeclaim \
    -l "app=sparkwing-cache-pool,sparkwing.dev/managed=pool-manager,sparkwing.dev/pool=cache" \
    "$ownership_label" "$owner_token_label" --overwrite || label_status=1
  return "$label_status"
}

preflight() {
  local command_name
  for command_name in kubectl helm curl jq openssl; do
    require_command "$command_name"
  done
  [[ -n "$kube_context" ]] || die "SPARKWING_K8S_E2E_KUBE_CONTEXT is required"
  [[ -n "$image_prefix" ]] || die "SPARKWING_K8S_E2E_IMAGE_PREFIX is required"
  [[ "$image_prefix" =~ ^[a-z0-9][a-z0-9._:/-]*$ ]] || die "SPARKWING_K8S_E2E_IMAGE_PREFIX contains characters that are unsafe in an image repository"
  [[ -n "$image_tag" ]] || die "SPARKWING_K8S_E2E_TAG is required"
  [[ "$cleanup_allow" == "$namespace/$release_name" ]] || die "SPARKWING_K8S_E2E_ALLOW_CLEANUP must equal $namespace/$release_name"
  kubectl version --client >/dev/null
  helm version --short >/dev/null
  kubectl config get-contexts "$kube_context" >/dev/null 2>&1 || die "Kubernetes context '$kube_context' does not exist"
  kube version --request-timeout=10s >/dev/null || die "Kubernetes context '$kube_context' is unavailable"
}

usage() {
  cat <<'EOF'
usage: bin/k8s-e2e.sh [--preflight]

Runs the complete Sparkwing controller/runner golden path against an explicit
Kubernetes context. It requires caller-supplied image coordinates and an exact
namespace/release cleanup allow-list. It never creates or deletes cluster
infrastructure.

--preflight checks the tools and selected target without creating resources.
EOF
}

case "${1-}" in
  "") ;;
  --preflight)
    preflight
    echo "k8s-e2e: preflight passed"
    exit 0
    ;;
  -h|--help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

preflight

[[ "$namespace" =~ ^[a-z0-9][a-z0-9.-]{0,62}$ ]] || die "invalid namespace: $namespace"
[[ "$release_name" =~ ^[a-z0-9][a-z0-9.-]{0,52}$ ]] || die "invalid Helm release name: $release_name"
[[ "$image_tag" =~ ^[a-z0-9][a-z0-9._-]{0,127}$ ]] || die "invalid image tag: $image_tag"
[[ "$keep_resources" == "0" || "$keep_resources" == "1" ]] || die "SPARKWING_K8S_E2E_KEEP_RESOURCES must be 0 or 1"

image_prefix="${image_prefix%/}/"
case "$image_pull_policy" in
  Always|IfNotPresent|Never) ;;
  *) die "SPARKWING_K8S_E2E_PULL_POLICY must be Always, IfNotPresent, or Never" ;;
esac

if [[ -n "${SPARKWING_K8S_E2E_ARTIFACT_DIR:-}" ]]; then
  artifact_dir="$SPARKWING_K8S_E2E_ARTIFACT_DIR"
  mkdir -p "$artifact_dir"
else
  artifact_dir="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-k8s-e2e.XXXXXX")"
fi
artifact_dir="$(cd "$artifact_dir" && pwd -P)"
echo "k8s-e2e: diagnostics: $artifact_dir"
run_owner="$(openssl rand -hex 16)"
[[ "$run_owner" =~ ^[0-9a-f]{32}$ ]] || die "OpenSSL returned an invalid E2E ownership token"
owner_token_label="sparkwing.dev/e2e-owner=$run_owner"
ownership_selector="$ownership_label,$owner_token_label"
release_owner_description="sparkwing-e2e-owner=$run_owner"

stop_forwards() {
  local pid
  for pid in "${forward_pids[@]-}"; do
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" >/dev/null 2>&1 || true
    fi
  done
  forward_pids=()
}

api_get() {
  local path=$1
  curl --fail --silent --show-error --max-time 10 \
    -H "Authorization: Bearer $admin_token" \
    "http://127.0.0.1:${controller_port}${path}"
}

api_get_with_status() {
  local path=$1
  curl --silent --show-error --max-time 10 \
    -H "Authorization: Bearer $admin_token" \
    --write-out $'\n%{http_code}' \
    "http://127.0.0.1:${controller_port}${path}"
}

api_post() {
  local path=$1
  curl --fail --silent --show-error --max-time 10 \
    -X POST -H "Authorization: Bearer $admin_token" \
    "http://127.0.0.1:${controller_port}${path}"
}

api_post_json() {
  local path=$1
  local body=$2
  curl --fail --silent --show-error --max-time 10 \
    -X POST -H "Authorization: Bearer $admin_token" \
    -H 'Content-Type: application/json' --data "$body" \
    "http://127.0.0.1:${controller_port}${path}"
}

collect_diagnostics() {
  local pod
  echo "k8s-e2e: collecting failure diagnostics in $artifact_dir" >&2
  mkdir -p "$artifact_dir/kubernetes" "$artifact_dir/pod-logs"
  helm_e2e get values "$release_name" --namespace "$namespace" --all >"$artifact_dir/kubernetes/helm-values-live.yaml" 2>&1 || true
  helm_e2e get manifest "$release_name" --namespace "$namespace" >"$artifact_dir/kubernetes/helm-manifest-live.yaml" 2>&1 || true
  kube --namespace "$namespace" get all,pvc,jobs -o wide >"$artifact_dir/kubernetes/resources.txt" 2>&1 || true
  kube --namespace "$namespace" get events --sort-by=.metadata.creationTimestamp >"$artifact_dir/kubernetes/events.txt" 2>&1 || true
  kube --namespace "$namespace" describe deployments,pods,pvc,jobs >"$artifact_dir/kubernetes/describe.txt" 2>&1 || true
  while IFS= read -r pod; do
    [[ -n "$pod" ]] || continue
    kube --namespace "$namespace" logs "$pod" --all-containers --timestamps >"$artifact_dir/pod-logs/${pod#pod/}.log" 2>&1 || true
    kube --namespace "$namespace" logs "$pod" --all-containers --timestamps --previous >"$artifact_dir/pod-logs/${pod#pod/}.previous.log" 2>&1 || true
  done < <(kube --namespace "$namespace" get pods -o name 2>/dev/null || true)
  if [[ -n "$admin_token" && -n "$controller_port" ]]; then
    api_get "/api/v1/runs?limit=100" >"$artifact_dir/kubernetes/runs.json" 2>&1 || true
    api_get "/api/v1/agents" >"$artifact_dir/kubernetes/agents.json" 2>&1 || true
  fi
  kube cluster-info dump --namespaces "$namespace" \
    --output-directory="$artifact_dir/kubernetes/cluster-dump" >/dev/null 2>&1 || true
}

cleanup_existing_resources() {
  local namespace_owner release_list release_present cleanup_status=0
  [[ "$cleanup_allow" == "$namespace/$release_name" ]] || {
    echo "k8s-e2e: refusing cleanup: allow-list no longer equals $namespace/$release_name" >&2
    return 1
  }
  namespace_owner="$(kube get namespace "$namespace" \
    -o 'jsonpath={.metadata.labels.sparkwing\.dev/e2e-owned}{"\t"}{.metadata.labels.sparkwing\.dev/e2e-owner}' 2>/dev/null)" || {
    echo "k8s-e2e: refusing cleanup: cannot verify namespace $namespace" >&2
    return 1
  }
  [[ "$namespace_owner" == $'true\t'"$run_owner" ]] || {
    echo "k8s-e2e: refusing cleanup: namespace $namespace is not owned by this run" >&2
    return 1
  }

  if ((release_install_attempted == 1)); then
    release_owned=0
    if ! release_list="$(helm_e2e list --namespace "$namespace" \
      --selector "$owner_token_label" -o json)"; then
      echo "k8s-e2e: refusing cleanup: cannot verify Helm release ownership" >&2
      return 1
    elif ! release_present="$(jq -r --arg name "$release_name" \
      'if type == "array" then any(.[]; .name == $name) else error("Helm list was not an array") end' \
      <<<"$release_list")"; then
      echo "k8s-e2e: refusing cleanup: Helm release ownership response was invalid" >&2
      return 1
    fi
    [[ "$release_present" == "true" ]] || {
      echo "k8s-e2e: refusing cleanup: release $release_name is not owned by this run" >&2
      return 1
    }
    # safety: Helm overwrites failed release descriptions, but owner labels remain queryable.
    release_owned=1
  fi
  if ((release_owned == 1)); then
    # safety: Prove Helm ownership before labeling retained PVCs or uninstalling.
    if ! label_owned_release_pvcs; then
      echo "k8s-e2e: refusing cleanup: could not label all owned PVCs" >&2
      return 1
    fi
    if helm_e2e uninstall "$release_name" --namespace "$namespace" --timeout 5m; then
      release_owned=0
      release_installed=0
    else
      cleanup_status=1
    fi
  fi
  kube --namespace "$namespace" delete \
    deployment,service,configmap,secret,persistentvolumeclaim \
    -l "$ownership_selector" --ignore-not-found --wait=true || cleanup_status=1
  return "$cleanup_status"
}

finish() {
  local status=$? cleanup_failed=0
  trap - EXIT
  set +e
  if ((status != 0)) && ((namespace_owned == 1)); then
    collect_diagnostics
  fi
  stop_forwards
  if ((namespace_owned == 1)); then
    if [[ "$keep_resources" == "1" ]]; then
      echo "k8s-e2e: preserving resources in namespace $namespace" >&2
    elif ! cleanup_existing_resources; then
      cleanup_failed=1
      echo "k8s-e2e: cleanup failed; retained namespace: $namespace" >&2
    fi
  fi
  if ((cleanup_failed == 1)) && ((status == 0)); then
    status=1
  fi
  if ((status == 0)); then
    echo "k8s-e2e: passed; evidence: $artifact_dir"
  else
    echo "k8s-e2e: failed; diagnostics: $artifact_dir" >&2
  fi
  exit "$status"
}

trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "k8s-e2e: using Kubernetes context $kube_context"

namespace_resource="$(kube get namespace "$namespace" --ignore-not-found -o name)" || \
  die "failed to check whether namespace '$namespace' exists"
if [[ -n "$namespace_resource" ]]; then
  die "namespace '$namespace' already exists; choose a dedicated empty namespace"
fi
create_owned_namespace
namespace_owned=1
create_owned_secret sparkwing-webhook \
  --from-literal="webhook-secret=$webhook_secret"
create_owned_secret sparkwing-secrets-key \
  --from-literal="key=$(openssl rand -base64 32)"

fixture_root="$repo_root/testdata/k8s-e2e/repo/.sparkwing"
create_owned_configmap sparkwing-k8s-fixture \
  --from-file="go.mod=$fixture_root/go.mod" \
  --from-file="go.sum=$fixture_root/go.sum" \
  --from-file="main.go=$fixture_root/main.go" \
  --from-file="k8s.go=$fixture_root/jobs/k8s.go" \
  --from-file="k8s_test.go=$fixture_root/jobs/k8s_test.go"

cat >"$artifact_dir/git-fixture.yaml" <<EOF
apiVersion: v1
kind: Service
metadata:
  name: k8s-repo
  namespace: $namespace
  labels:
    app.kubernetes.io/name: sparkwing-k8s-repo
    sparkwing.dev/e2e-owned: "true"
    sparkwing.dev/e2e-owner: "$run_owner"
spec:
  selector:
    app.kubernetes.io/name: sparkwing-k8s-repo
  ports:
    - name: git
      port: 9418
      targetPort: git
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: k8s-repo
  namespace: $namespace
  labels:
    app.kubernetes.io/name: sparkwing-k8s-repo
    sparkwing.dev/e2e-owned: "true"
    sparkwing.dev/e2e-owner: "$run_owner"
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: sparkwing-k8s-repo
  template:
    metadata:
      labels:
        app.kubernetes.io/name: sparkwing-k8s-repo
        sparkwing.dev/e2e-owned: "true"
        sparkwing.dev/e2e-owner: "$run_owner"
    spec:
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        runAsGroup: 65534
        fsGroup: 65534
      initContainers:
        - name: prepare
          image: $(image_ref sparkwing-runner)
          imagePullPolicy: $image_pull_policy
          command: ["/bin/sh", "-ec"]
          args:
            - |
              mkdir -p /work/source/.sparkwing/jobs
              cp /fixture/go.mod /fixture/go.sum /fixture/main.go /work/source/.sparkwing/
              cp /fixture/k8s.go /fixture/k8s_test.go /work/source/.sparkwing/jobs/
              git -C /work/source init --initial-branch=main
              git -C /work/source config user.name "Sparkwing Kubernetes E2E"
              git -C /work/source config user.email "kubernetes-e2e@sparkwing.invalid"
              git -C /work/source add .
              GIT_AUTHOR_DATE="2000-01-01T00:00:00Z" GIT_COMMITTER_DATE="2000-01-01T00:00:00Z" \
                git -C /work/source commit --message "test: add Kubernetes golden-path fixture"
              git clone --bare /work/source /work/e2e.git
              touch /work/e2e.git/git-daemon-export-ok
          volumeMounts:
            - name: source
              mountPath: /fixture
              readOnly: true
            - name: repository
              mountPath: /work
      containers:
        - name: git
          image: $(image_ref sparkwing-runner)
          imagePullPolicy: $image_pull_policy
          command: ["git"]
          args: ["daemon", "--reuseaddr", "--base-path=/srv/git", "--export-all", "--verbose", "/srv/git"]
          ports:
            - name: git
              containerPort: 9418
          volumeMounts:
            - name: repository
              mountPath: /srv/git
              readOnly: true
          readinessProbe:
            tcpSocket:
              port: git
            initialDelaySeconds: 1
            periodSeconds: 1
      volumes:
        - name: source
          configMap:
            name: sparkwing-k8s-fixture
        - name: repository
          emptyDir: {}
EOF
kube apply -f "$artifact_dir/git-fixture.yaml"
kube --namespace "$namespace" rollout status deployment/k8s-repo --timeout=180s

write_bootstrap_values() {
  local path=$1
  cat >"$path" <<EOF
controller:
  image:
    repository: ${image_prefix}sparkwing-controller
    tag: $image_tag
    pullPolicy: $image_pull_policy
  githubWebhookSecret:
    name: sparkwing-webhook
  secretsKey:
    name: sparkwing-secrets-key
  storage:
    type: pvc
    pvc:
      keepOnUninstall: true
web:
  image:
    repository: ${image_prefix}sparkwing-web
    tag: $image_tag
    pullPolicy: $image_pull_policy
sparkwing-runner-bundle:
  runner:
    replicas: 1
    image:
      repository: ${image_prefix}sparkwing-runner
      tag: $image_tag
      pullPolicy: $image_pull_policy
    labels: [cluster]
    maxClaimsBeforeRestart: 0
    alsoClaimTriggers: true
  cache:
    allowUnauthenticated: true
    image:
      repository: ${image_prefix}sparkwing-cache
      tag: $image_tag
      pullPolicy: $image_pull_policy
    dependencyProxy:
      enabled: false
    storage:
      enabled: true
      keepOnUninstall: true
  logs:
    allowUnauthenticated: true
    image:
      repository: ${image_prefix}sparkwing-logs
      tag: $image_tag
      pullPolicy: $image_pull_policy
    storage:
      enabled: true
      keepOnUninstall: true
EOF
}

bootstrap_values="$artifact_dir/values-bootstrap.yaml"
authenticated_values="$artifact_dir/values-authenticated.yaml"
write_bootstrap_values "$bootstrap_values"
cat >"$authenticated_values" <<'EOF'
controller:
  extraEnv:
    - name: SPARKWING_REQUIRE_AUTH
      value: "true"
web:
  tokenSecret:
    name: sparkwing-token
sparkwing-runner-bundle:
  controller:
    tokenSecret:
      name: sparkwing-token
  cache:
    allowUnauthenticated: false
  logs:
    allowUnauthenticated: false
EOF

helm_e2e lint "$repo_root/charts/sparkwing-full" -f "$bootstrap_values"
helm_e2e template "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" -f "$bootstrap_values" -f "$authenticated_values" >"$artifact_dir/rendered-chart.yaml"
release_install_attempted=1
helm_e2e install "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" --description "$release_owner_description" --labels "$owner_token_label" \
  -f "$bootstrap_values" --timeout 5m --wait
release_installed=1
release_owned=1
label_owned_release_pvcs

resource_name() {
  local resource=$1
  local component=$2
  local names
  names="$(kube --namespace "$namespace" get "$resource" \
    -l "app.kubernetes.io/instance=$release_name,app.kubernetes.io/component=$component" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
  [[ "$(printf '%s\n' "$names" | sed '/^$/d' | wc -l | tr -d ' ')" == "1" ]] || \
    die "expected one $resource for component $component, got: $names"
  printf '%s' "$names"
}

ready_pod_identity() {
  local component=$1
  kube --namespace "$namespace" get pods \
    -l "app.kubernetes.io/instance=$release_name,app.kubernetes.io/component=$component" \
    -o json | jq -er '
      [.items[] | select(any(.status.conditions[]?; .type == "Ready" and .status == "True"))]
      | sort_by(.metadata.creationTimestamp)
      | last
      | [.metadata.name, .metadata.uid]
      | @tsv'
}

controller_deployment="$(resource_name deployment controller)"
controller_service="$(resource_name service controller)"
web_deployment="$(resource_name deployment web)"
web_service="$(resource_name service web)"
runner_deployment="$(resource_name deployment runner)"
cache_deployment="$(resource_name deployment cache)"
controller_pvc="$(resource_name pvc controller)"

kube --namespace "$namespace" wait deployment \
  -l "app.kubernetes.io/instance=$release_name" \
  --for=condition=Available --timeout=300s

start_forward() {
  local service=$1
  local remote_port=$2
  local label=$3
  local log="$artifact_dir/port-forward-$label.log"
  local pid port
  kube --namespace "$namespace" port-forward "service/$service" ":$remote_port" >"$log" 2>&1 &
  pid=$!
  forward_pids+=("$pid")
  for _ in {1..100}; do
    port="$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$log" | head -n 1)"
    if [[ -n "$port" ]]; then
      forward_port=$port
      return 0
    fi
    kill -0 "$pid" >/dev/null 2>&1 || die "port-forward for $service exited; see $log"
    sleep 0.1
  done
  die "port-forward for $service did not become ready; see $log"
}

start_forward "$controller_service" 80 controller-bootstrap
controller_port=$forward_port
for _ in {1..100}; do
  if curl --fail --silent --show-error --max-time 2 \
    "http://127.0.0.1:${controller_port}/api/v1/health" >/dev/null; then
    break
  fi
  sleep 0.2
done

token_response="$(curl --fail --silent --show-error --max-time 10 \
  -H 'Content-Type: application/json' \
  --data '{"kind":"user","principal":"k8s-e2e-admin","scopes":["admin"]}' \
  "http://127.0.0.1:${controller_port}/api/v1/tokens")"
admin_token="$(jq -er '.token' <<<"$token_response")"
[[ "$admin_token" == sw* ]] || die "controller returned an invalid bootstrap token"

create_owned_secret sparkwing-token \
  --from-literal="token=$admin_token"
helm_e2e upgrade "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" -f "$bootstrap_values" -f "$authenticated_values" --timeout 5m --wait
kube --namespace "$namespace" wait deployment \
  -l "app.kubernetes.io/instance=$release_name" \
  --for=condition=Available --timeout=300s
stop_forwards
controller_port=""
start_forward "$controller_service" 80 controller-authenticated
controller_port=$forward_port

unauthenticated_status="$(curl --silent --show-error --max-time 5 \
  -o /dev/null -w '%{http_code}' \
  "http://127.0.0.1:${controller_port}/api/v1/runs")"
[[ "$unauthenticated_status" == "401" ]] || die "protected controller read returned $unauthenticated_status without a token"
api_get "/api/v1/runs?limit=1" >/dev/null

for repository_source in \
  "https://github.com/sparkwing-k8s/" \
  "git@github.com:sparkwing-k8s/"; do
  kube --namespace "$namespace" exec "deployment/$cache_deployment" -- \
    git config --global --add url."git://k8s-repo.${namespace}.svc.cluster.local/".insteadOf \
    "$repository_source"
done
fixture_sha="$(kube --namespace "$namespace" exec "deployment/$cache_deployment" -- \
  git ls-remote "https://github.com/sparkwing-k8s/e2e.git" refs/heads/main | awk '{print $1}')"
[[ "$fixture_sha" =~ ^[0-9a-f]{40}$ ]] || die "in-cluster Git returned invalid fixture commit $fixture_sha"

webhook_payload() {
  jq -nc --arg sha "$fixture_sha" --arg nonce "${1:-0}" '{
    ref:"refs/heads/main",
    before:"0000000000000000000000000000000000000000",
    after:$sha,
    deleted:false,
    repository:{full_name:"sparkwing-k8s/e2e"},
    pusher:{name:"k8s-e2e",email:"k8s-e2e@sparkwing.invalid"},
    head_commit:{id:$sha,message:("Kind golden path " + $nonce)}
  }'
}

webhook_sequence=0
send_webhook() {
  local pipeline=$1
  local payload signature response
  webhook_sequence=$((webhook_sequence + 1))
  payload="$(webhook_payload "$webhook_sequence")"
  signature="$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$webhook_secret" -hex | awk '{print $NF}')"
  response="$(curl --fail --silent --show-error --max-time 10 \
    -H 'Content-Type: application/json' \
    -H 'X-GitHub-Event: push' \
    -H "X-GitHub-Delivery: k8s-e2e-${webhook_sequence}" \
    -H "X-Hub-Signature-256: sha256=$signature" \
    --data "$payload" \
    "http://127.0.0.1:${controller_port}/webhooks/github/${pipeline}")"
  webhook_run_id="$(jq -er '.run_id' <<<"$response")"
}

runner_probe_sequence=0
prove_runner_claim() {
  local runner_pod=$1
  local phase=$2
  local run_id node_id now nodes claim
  runner_probe_sequence=$((runner_probe_sequence + 1))
  run_id="k8s-runner-probe-${run_owner:0:12}-${runner_probe_sequence}"
  node_id="runner-claim"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  api_post_json "/api/v1/runs" "$(jq -nc \
    --arg id "$run_id" --arg started "$now" \
    '{id:$id,pipeline:"k8s-runner-probe",status:"running",trigger_source:"k8s-e2e",started_at:$started}')" >/dev/null
  api_post_json "/api/v1/runs/$run_id/nodes" \
    '{"id":"runner-claim","status":"pending","deps":[],"needs_labels":["cluster"]}' >/dev/null
  api_post "/api/v1/runs/$run_id/nodes/$node_id/mark-ready" >/dev/null
  for _ in {1..120}; do
    nodes="$(api_get "/api/v1/runs/$run_id/nodes")"
    claim="$(jq -r '.nodes[0].claimed_by // empty' <<<"$nodes")"
    if [[ "$claim" == "runner:${runner_pod}:"* ]]; then
      runner_probe_run_id=$run_id
      runner_probe_claim=$claim
      return 0
    fi
    [[ -z "$claim" ]] || die "$phase claim $claim does not identify Ready runner pod $runner_pod"
    sleep 0.5
  done
  die "$phase node $run_id/$node_id was not claimed by Ready runner pod $runner_pod"
}

wait_run_status() {
  local run_id=$1
  local wanted=$2
  local timeout_seconds=$3
  local deadline=$((SECONDS + timeout_seconds))
  local body http_status response status
  while ((SECONDS < deadline)); do
    if ! response="$(api_get_with_status "/api/v1/runs/$run_id")"; then
      die "run $run_id lookup failed while waiting for $wanted"
    fi
    http_status="${response##*$'\n'}"
    body="${response%$'\n'*}"
    case "$http_status" in
      200) ;;
      404)
        sleep 1
        continue
        ;;
      *) die "run $run_id returned HTTP $http_status while waiting for $wanted" ;;
    esac
    status="$(jq -er '.status' <<<"$body")"
    if [[ "$status" == "$wanted" ]]; then
      return 0
    fi
    case "$status" in
      done|success|failed|cancelled|skipped)
        die "run $run_id reached $status while waiting for $wanted"
        ;;
    esac
    sleep 1
  done
  die "run $run_id did not reach $wanted within ${timeout_seconds}s"
}

echo "k8s-e2e: proving invalid webhook authentication"
api_get "/api/v1/runs?limit=100" | jq -e '.runs | length == 0' >/dev/null
invalid_webhook_payload="$(webhook_payload)"
invalid_webhook_status="$(curl --silent --show-error --max-time 10 \
  -o "$artifact_dir/invalid-webhook-response.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: push' \
  -H 'X-GitHub-Delivery: k8s-e2e-invalid' \
  -H 'X-Hub-Signature-256: sha256=0000000000000000000000000000000000000000000000000000000000000000' \
  --data "$invalid_webhook_payload" \
  "http://127.0.0.1:${controller_port}/webhooks/github/k8s-success")"
[[ "$invalid_webhook_status" == "401" ]] || die "invalid webhook returned $invalid_webhook_status, want 401"
api_get "/api/v1/runs?limit=100" | jq -e '.runs | length == 0' >/dev/null

echo "k8s-e2e: proving valid webhook, trigger claim, node execution, and web proxies"
IFS=$'\t' read -r initial_runner_pod initial_runner_uid < <(ready_pod_identity runner)
send_webhook k8s-success
success_run=$webhook_run_id
wait_run_status "$success_run" success 300
success_nodes="$(api_get "/api/v1/runs/$success_run/nodes")"
jq -e '.nodes | length == 1 and .[0].status == "done"' <<<"$success_nodes" >/dev/null
success_started="$(jq -er '.nodes[0].started_at' <<<"$success_nodes")"
prove_runner_claim "$initial_runner_pod" "initial runner"
initial_runner_probe_run=$runner_probe_run_id
initial_runner_claim=$runner_probe_claim
agents="$(api_get "/api/v1/agents")"
jq -e --arg name "$initial_runner_pod" \
  '.agents | any(.type == "agent" and .name == $name)' <<<"$agents" >/dev/null

start_forward "$web_service" 80 web
web_port=$forward_port
web_root="$artifact_dir/web-root.html"
web_static_asset="$artifact_dir/web-static-asset"
curl --fail --silent --show-error --location --max-time 10 \
  "http://127.0.0.1:${web_port}/" >"$web_root"
web_static_path="$(grep -Eo '/_next/static/[^"<> ]+' "$web_root" | sed -n '1p')"
[[ "$web_static_path" == /_next/static/* ]] || die "web root did not reference a built static asset"
curl --fail --silent --show-error --location --max-time 10 \
  "http://127.0.0.1:${web_port}${web_static_path}" >"$web_static_asset"
[[ -s "$web_static_asset" ]] || die "referenced web static asset was empty"
curl --fail --silent --show-error --max-time 10 \
  "http://127.0.0.1:${web_port}/api/v1/runs/$success_run" | jq -e '.status == "success"' >/dev/null
curl --fail --silent --show-error --max-time 10 \
  "http://127.0.0.1:${web_port}/api/v1/runs/$success_run/logs/prove-controller-runner-logs" | \
  grep -q "sparkwing-k8s-e2e-success run_id=$success_run"

echo "k8s-e2e: proving cancellation"
send_webhook k8s-slow
cancelled_run=$webhook_run_id
wait_run_status "$cancelled_run" running 180
curl --fail --silent --show-error --max-time 10 \
  -X POST -H "Authorization: Bearer $admin_token" \
  "http://127.0.0.1:${controller_port}/api/v1/runs/$cancelled_run/cancel" >/dev/null
wait_run_status "$cancelled_run" cancelled 120

echo "k8s-e2e: proving runner restart and a fresh claim"
runner_pod_before=$initial_runner_pod
runner_uid_before=$initial_runner_uid
kube --namespace "$namespace" rollout restart "deployment/$runner_deployment"
kube --namespace "$namespace" rollout status "deployment/$runner_deployment" --timeout=300s
kube --namespace "$namespace" get "deployment/$runner_deployment" -o json | \
  jq -e '.spec.replicas == 1 and .status.readyReplicas == 1 and .status.updatedReplicas == 1 and (.status.unavailableReplicas // 0) == 0' >/dev/null
IFS=$'\t' read -r runner_pod_after runner_uid_after < <(ready_pod_identity runner)
[[ "$runner_uid_after" != "$runner_uid_before" ]] || die "runner rollout did not replace its pod"
[[ "$runner_pod_after" != "$runner_pod_before" ]] || die "runner rollout did not replace its pod name"
send_webhook k8s-success
post_runner_restart_run=$webhook_run_id
wait_run_status "$post_runner_restart_run" success 300
post_runner_nodes="$(api_get "/api/v1/runs/$post_runner_restart_run/nodes")"
jq -e '.nodes | length == 1 and .[0].status == "done"' <<<"$post_runner_nodes" >/dev/null
prove_runner_claim "$runner_pod_after" "post-restart runner"
post_runner_probe_run=$runner_probe_run_id
post_runner_claim=$runner_probe_claim

echo "k8s-e2e: proving controller restart and retained run state"
IFS=$'\t' read -r controller_pod_before controller_uid_before < <(ready_pod_identity controller)
kube --namespace "$namespace" rollout restart "deployment/$controller_deployment"
kube --namespace "$namespace" rollout status "deployment/$controller_deployment" --timeout=300s
IFS=$'\t' read -r controller_pod_after controller_uid_after < <(ready_pod_identity controller)
[[ "$controller_uid_after" != "$controller_uid_before" ]] || die "controller rollout did not replace its pod"
[[ "$controller_pod_after" != "$controller_pod_before" ]] || die "controller rollout did not replace its pod name"
stop_forwards
controller_port=""
web_port=""
start_forward "$controller_service" 80 controller-restarted
controller_port=$forward_port
start_forward "$web_service" 80 web-controller-restarted
web_port=$forward_port
api_get "/api/v1/runs/$success_run" | jq -e '.status == "success"' >/dev/null

echo "k8s-e2e: proving retry"
retry_response="$(curl --fail --silent --show-error --max-time 10 \
  -X POST -H "Authorization: Bearer $admin_token" \
  "http://127.0.0.1:${controller_port}/api/v1/runs/$success_run/retry?full=1")"
retry_run="$(jq -er '.id' <<<"$retry_response")"
wait_run_status "$retry_run" success 300
api_get "/api/v1/runs/$retry_run" | jq -e --arg source "$success_run" '.retry_of == $source' >/dev/null
retry_nodes="$(api_get "/api/v1/runs/$retry_run/nodes")"
jq -e --arg source_started "$success_started" '
  .nodes | length == 1
  and .[0].status == "done"
  and .[0].started_at != null
  and .[0].finished_at != null
  and .[0].started_at != $source_started
' <<<"$retry_nodes" >/dev/null
retry_output="$(api_get "/api/v1/runs/$retry_run/nodes/prove-controller-runner-logs/output" | jq -er '.')"
[[ "$retry_output" == "$retry_run" ]] || die "retry node output $retry_output does not match retry run $retry_run"
curl --fail --silent --show-error --max-time 10 \
  "http://127.0.0.1:${web_port}/api/v1/runs/$retry_run/logs/prove-controller-runner-logs" | \
  grep -q "sparkwing-k8s-e2e-success run_id=$retry_run"

echo "k8s-e2e: proving uninstall retention and reinstall recovery"
controller_pvc_uid="$(kube --namespace "$namespace" get pvc "$controller_pvc" -o jsonpath='{.metadata.uid}')"
stop_forwards
controller_port=""
web_port=""
helm_e2e uninstall "$release_name" --namespace "$namespace" --timeout 5m
release_installed=0
release_owned=0
release_install_attempted=0
retained_pvc_uid="$(kube --namespace "$namespace" get pvc "$controller_pvc" -o jsonpath='{.metadata.uid}')"
[[ "$retained_pvc_uid" == "$controller_pvc_uid" ]] || die "controller PVC was not retained across uninstall"

release_install_attempted=1
helm_e2e install "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" --description "$release_owner_description" --labels "$owner_token_label" \
  -f "$bootstrap_values" -f "$authenticated_values" --timeout 5m --wait
release_installed=1
release_owned=1
kube --namespace "$namespace" wait deployment \
  -l "app.kubernetes.io/instance=$release_name" \
  --for=condition=Available --timeout=300s
start_forward "$controller_service" 80 controller-reinstall
controller_port=$forward_port
start_forward "$web_service" 80 web-reinstall
web_port=$forward_port
api_get "/api/v1/runs/$success_run" | jq -e '.status == "success"' >/dev/null
curl --fail --silent --show-error --max-time 10 \
  "http://127.0.0.1:${web_port}/api/v1/runs/$success_run/logs/prove-controller-runner-logs" | \
  grep -q "sparkwing-k8s-e2e-success run_id=$success_run"

cat >"$artifact_dir/result.txt" <<EOF
status=success
kube_context=$kube_context
namespace=$namespace
release=$release_name
image_prefix=$image_prefix
image_tag=$image_tag
success_run=$success_run
initial_runner_probe_run=$initial_runner_probe_run
initial_runner_claim=$initial_runner_claim
cancelled_run=$cancelled_run
post_runner_restart_run=$post_runner_restart_run
post_runner_probe_run=$post_runner_probe_run
post_runner_claim=$post_runner_claim
retry_run=$retry_run
controller_pvc=$controller_pvc
fixture_sha=$fixture_sha
EOF
