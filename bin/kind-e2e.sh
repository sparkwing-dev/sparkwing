#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cluster_name="${SPARKWING_KIND_E2E_CLUSTER:-sparkwing-e2e}"
namespace="${SPARKWING_KIND_E2E_NAMESPACE:-sparkwing-e2e}"
release_name="${SPARKWING_KIND_E2E_RELEASE:-sparkwing}"
image_tag="${SPARKWING_KIND_E2E_TAG:-kind-e2e}"
keep_cluster="${SPARKWING_KIND_E2E_KEEP_CLUSTER:-0}"
webhook_secret="sparkwing-kind-webhook"
artifact_dir=""
cluster_owned=0
release_installed=0
admin_token=""
controller_port=""
web_port=""
forward_pids=()

components=(
  sparkwing-controller
  sparkwing-web
  sparkwing-logs
  sparkwing-cache
  sparkwing-runner
)
dockerfiles=(
  build/Dockerfile.binary
  build/Dockerfile.binary
  build/Dockerfile.binary
  build/Dockerfile.binary
  build/Dockerfile.runner
)

die() {
  echo "kind-e2e: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command '$1' is not installed"
}

preflight() {
  local command_name
  for command_name in docker kind kubectl helm curl jq git openssl; do
    require_command "$command_name"
  done
  docker info >/dev/null 2>&1 || die "Docker daemon is unavailable; start Docker Desktop, Colima, or dockerd, then retry"
  docker buildx version >/dev/null 2>&1 || die "docker buildx is unavailable"
  kind version >/dev/null
  kubectl version --client >/dev/null
  helm version --short >/dev/null
}

usage() {
  cat <<'EOF'
usage: bin/kind-e2e.sh [--preflight]

Runs the complete Sparkwing controller/runner golden path in a disposable
local Kind cluster. --preflight checks tools and the Docker daemon without
building images or creating resources.
EOF
}

case "${1-}" in
  "") ;;
  --preflight)
    preflight
    echo "kind-e2e: preflight passed"
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

[[ "$cluster_name" =~ ^[a-z0-9][a-z0-9.-]{0,47}$ ]] || die "invalid Kind cluster name: $cluster_name"
[[ "$namespace" =~ ^[a-z0-9][a-z0-9.-]{0,62}$ ]] || die "invalid namespace: $namespace"
[[ "$release_name" =~ ^[a-z0-9][a-z0-9.-]{0,52}$ ]] || die "invalid Helm release name: $release_name"
[[ "$image_tag" =~ ^[a-z0-9][a-z0-9._-]{0,127}$ ]] || die "invalid image tag: $image_tag"

if [[ -n "${SPARKWING_KIND_E2E_ARTIFACT_DIR:-}" ]]; then
  artifact_dir="$SPARKWING_KIND_E2E_ARTIFACT_DIR"
  mkdir -p "$artifact_dir"
else
  artifact_dir="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-kind-e2e.XXXXXX")"
fi
artifact_dir="$(cd "$artifact_dir" && pwd -P)"
echo "kind-e2e: diagnostics: $artifact_dir"

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

collect_diagnostics() {
  local pod
  echo "kind-e2e: collecting failure diagnostics in $artifact_dir" >&2
  mkdir -p "$artifact_dir/kubernetes" "$artifact_dir/pod-logs" "$artifact_dir/kind-logs"
  helm get values "$release_name" --namespace "$namespace" --all >"$artifact_dir/kubernetes/helm-values-live.yaml" 2>&1 || true
  helm get manifest "$release_name" --namespace "$namespace" >"$artifact_dir/kubernetes/helm-manifest-live.yaml" 2>&1 || true
  kubectl --namespace "$namespace" get all,pvc,jobs -o wide >"$artifact_dir/kubernetes/resources.txt" 2>&1 || true
  kubectl --namespace "$namespace" get events --sort-by=.metadata.creationTimestamp >"$artifact_dir/kubernetes/events.txt" 2>&1 || true
  kubectl --namespace "$namespace" describe deployments,pods,pvc,jobs >"$artifact_dir/kubernetes/describe.txt" 2>&1 || true
  while IFS= read -r pod; do
    [[ -n "$pod" ]] || continue
    kubectl --namespace "$namespace" logs "$pod" --all-containers --timestamps >"$artifact_dir/pod-logs/${pod#pod/}.log" 2>&1 || true
    kubectl --namespace "$namespace" logs "$pod" --all-containers --timestamps --previous >"$artifact_dir/pod-logs/${pod#pod/}.previous.log" 2>&1 || true
  done < <(kubectl --namespace "$namespace" get pods -o name 2>/dev/null || true)
  if [[ -n "$admin_token" && -n "$controller_port" ]]; then
    api_get "/api/v1/runs?limit=100" >"$artifact_dir/kubernetes/runs.json" 2>&1 || true
    api_get "/api/v1/agents" >"$artifact_dir/kubernetes/agents.json" 2>&1 || true
  fi
  kind export logs "$artifact_dir/kind-logs" --name "$cluster_name" >/dev/null 2>&1 || true
}

finish() {
  local status=$?
  trap - EXIT
  set +e
  if ((status != 0)) && ((cluster_owned == 1)); then
    collect_diagnostics
  fi
  stop_forwards
  if ((cluster_owned == 1)); then
    if [[ "$keep_cluster" == "1" ]]; then
      echo "kind-e2e: preserving cluster $cluster_name" >&2
    else
      if ((release_installed == 1)); then
        helm uninstall "$release_name" --namespace "$namespace" >/dev/null 2>&1 || true
      fi
      kind delete cluster --name "$cluster_name" >/dev/null 2>&1 || true
    fi
  fi
  if ((status == 0)); then
    echo "kind-e2e: passed; evidence: $artifact_dir"
  else
    echo "kind-e2e: failed; diagnostics: $artifact_dir" >&2
  fi
  exit "$status"
}

trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if kind get clusters | grep -Fxq "$cluster_name"; then
  die "Kind cluster '$cluster_name' already exists; choose SPARKWING_KIND_E2E_CLUSTER or remove it explicitly"
fi

echo "kind-e2e: building dashboard bundle"
bash "$repo_root/bin/build-web.sh"

echo "kind-e2e: building five release-shaped images"
for i in "${!components[@]}"; do
  component=${components[$i]}
  dockerfile=${dockerfiles[$i]}
  docker buildx build \
    --load \
    --file "$repo_root/$dockerfile" \
    --build-arg "BINARY=$component" \
    --build-arg "SPARKWING_VERSION=$image_tag" \
    --tag "$component:$image_tag" \
    "$repo_root"
done

fixture_work="$artifact_dir/fixture-work"
fixture_bare="$artifact_dir/fixture-bare"
mkdir -p "$fixture_work" "$fixture_bare"
cp -R "$repo_root/testdata/kind-e2e/repo/." "$fixture_work/"
git -C "$fixture_work" init --initial-branch=main
git -C "$fixture_work" config user.name "Sparkwing Kind E2E"
git -C "$fixture_work" config user.email "kind-e2e@sparkwing.invalid"
git -C "$fixture_work" add .
GIT_AUTHOR_DATE="2000-01-01T00:00:00Z" GIT_COMMITTER_DATE="2000-01-01T00:00:00Z" \
  git -C "$fixture_work" commit --message "test: add Kind golden-path fixture"
git clone --bare "$fixture_work" "$fixture_bare/e2e.git"
touch "$fixture_bare/e2e.git/git-daemon-export-ok"
chmod -R a+rX "$fixture_bare"
fixture_sha="$(git -C "$fixture_work" rev-parse HEAD)"

kind_config="$artifact_dir/kind-config.yaml"
cat >"$kind_config" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
    extraMounts:
      - hostPath: $fixture_bare
        containerPath: /sparkwing-kind-fixture
        readOnly: true
EOF

echo "kind-e2e: creating Kind cluster $cluster_name"
cluster_owned=1
kind create cluster --name "$cluster_name" --config "$kind_config" --wait 180s

echo "kind-e2e: loading images"
images=()
for component in "${components[@]}"; do
  images+=("$component:$image_tag")
done
kind load docker-image --name "$cluster_name" "${images[@]}"

kubectl create namespace "$namespace"
kubectl --namespace "$namespace" create secret generic sparkwing-webhook \
  --from-literal="webhook-secret=$webhook_secret"
kubectl --namespace "$namespace" create secret generic sparkwing-secrets-key \
  --from-literal="key=$(openssl rand -base64 32)"

cat >"$artifact_dir/git-fixture.yaml" <<EOF
apiVersion: v1
kind: Service
metadata:
  name: kind-repo
  namespace: $namespace
  labels:
    app.kubernetes.io/name: sparkwing-kind-repo
spec:
  selector:
    app.kubernetes.io/name: sparkwing-kind-repo
  ports:
    - name: git
      port: 9418
      targetPort: git
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kind-repo
  namespace: $namespace
  labels:
    app.kubernetes.io/name: sparkwing-kind-repo
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: sparkwing-kind-repo
  template:
    metadata:
      labels:
        app.kubernetes.io/name: sparkwing-kind-repo
    spec:
      containers:
        - name: git
          image: sparkwing-runner:$image_tag
          imagePullPolicy: Never
          command: ["git"]
          args: ["daemon", "--reuseaddr", "--base-path=/srv/git", "--export-all", "--verbose", "/srv/git"]
          ports:
            - name: git
              containerPort: 9418
          volumeMounts:
            - name: fixture
              mountPath: /srv/git
              readOnly: true
      volumes:
        - name: fixture
          hostPath:
            path: /sparkwing-kind-fixture
            type: Directory
EOF
kubectl apply -f "$artifact_dir/git-fixture.yaml"
kubectl --namespace "$namespace" rollout status deployment/kind-repo --timeout=180s

write_bootstrap_values() {
  local path=$1
  cat >"$path" <<EOF
controller:
  image:
    repository: sparkwing-controller
    tag: $image_tag
    pullPolicy: Never
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
    repository: sparkwing-web
    tag: $image_tag
    pullPolicy: Never
sparkwing-runner-bundle:
  runner:
    replicas: 1
    image:
      repository: sparkwing-runner
      tag: $image_tag
      pullPolicy: Never
    labels: [cluster]
    maxClaimsBeforeRestart: 0
    alsoClaimTriggers: true
  cache:
    image:
      repository: sparkwing-cache
      tag: $image_tag
      pullPolicy: Never
    dependencyProxy:
      enabled: false
    storage:
      enabled: true
      keepOnUninstall: true
  logs:
    image:
      repository: sparkwing-logs
      tag: $image_tag
      pullPolicy: Never
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
EOF

helm lint "$repo_root/charts/sparkwing-full" -f "$bootstrap_values"
helm template "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" -f "$bootstrap_values" -f "$authenticated_values" >"$artifact_dir/rendered-chart.yaml"
helm install "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" -f "$bootstrap_values" --timeout 5m --wait
release_installed=1

resource_name() {
  local resource=$1
  local component=$2
  local names
  names="$(kubectl --namespace "$namespace" get "$resource" \
    -l "app.kubernetes.io/instance=$release_name,app.kubernetes.io/component=$component" \
    -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')"
  [[ "$(printf '%s\n' "$names" | sed '/^$/d' | wc -l | tr -d ' ')" == "1" ]] || \
    die "expected one $resource for component $component, got: $names"
  printf '%s' "$names"
}

ready_pod_identity() {
  local component=$1
  kubectl --namespace "$namespace" get pods \
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

kubectl --namespace "$namespace" wait deployment \
  -l "app.kubernetes.io/instance=$release_name" \
  --for=condition=Available --timeout=300s

start_forward() {
  local service=$1
  local remote_port=$2
  local label=$3
  local log="$artifact_dir/port-forward-$label.log"
  local pid port
  kubectl --namespace "$namespace" port-forward "service/$service" ":$remote_port" >"$log" 2>&1 &
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
  --data '{"kind":"user","principal":"kind-e2e-admin","scopes":["admin"]}' \
  "http://127.0.0.1:${controller_port}/api/v1/tokens")"
admin_token="$(jq -er '.token' <<<"$token_response")"
[[ "$admin_token" == sw* ]] || die "controller returned an invalid bootstrap token"

kubectl --namespace "$namespace" create secret generic sparkwing-token \
  --from-literal="token=$admin_token"
helm upgrade "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" -f "$bootstrap_values" -f "$authenticated_values" --timeout 5m --wait
kubectl --namespace "$namespace" wait deployment \
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

kubectl --namespace "$namespace" exec "deployment/$cache_deployment" -- \
  git config --global url."git://kind-repo.${namespace}.svc.cluster.local/".insteadOf \
  "https://github.com/sparkwing-kind/"
resolved_fixture_sha="$(kubectl --namespace "$namespace" exec "deployment/$cache_deployment" -- \
  git ls-remote "https://github.com/sparkwing-kind/e2e.git" refs/heads/main | awk '{print $1}')"
[[ "$resolved_fixture_sha" == "$fixture_sha" ]] || \
  die "in-cluster Git resolved $resolved_fixture_sha, want fixture commit $fixture_sha"

webhook_payload() {
  jq -nc --arg sha "$fixture_sha" '{
    ref:"refs/heads/main",
    before:"0000000000000000000000000000000000000000",
    after:$sha,
    deleted:false,
    repository:{full_name:"sparkwing-kind/e2e"},
    pusher:{name:"kind-e2e",email:"kind-e2e@sparkwing.invalid"},
    head_commit:{id:$sha,message:"Kind golden path"}
  }'
}

webhook_sequence=0
send_webhook() {
  local pipeline=$1
  local payload signature response
  webhook_sequence=$((webhook_sequence + 1))
  payload="$(webhook_payload)"
  signature="$(printf '%s' "$payload" | openssl dgst -sha256 -hmac "$webhook_secret" -hex | awk '{print $NF}')"
  response="$(curl --fail --silent --show-error --max-time 10 \
    -H 'Content-Type: application/json' \
    -H 'X-GitHub-Event: push' \
    -H "X-GitHub-Delivery: kind-e2e-${webhook_sequence}" \
    -H "X-Hub-Signature-256: sha256=$signature" \
    --data "$payload" \
    "http://127.0.0.1:${controller_port}/webhooks/github/${pipeline}")"
  webhook_run_id="$(jq -er '.run_id' <<<"$response")"
}

wait_run_status() {
  local run_id=$1
  local wanted=$2
  local timeout_seconds=$3
  local deadline=$((SECONDS + timeout_seconds))
  local body status
  while ((SECONDS < deadline)); do
    body="$(api_get "/api/v1/runs/$run_id")"
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

echo "kind-e2e: proving invalid webhook authentication"
api_get "/api/v1/runs?limit=100" | jq -e '.runs | length == 0' >/dev/null
invalid_webhook_payload="$(webhook_payload)"
invalid_webhook_status="$(curl --silent --show-error --max-time 10 \
  -o "$artifact_dir/invalid-webhook-response.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: push' \
  -H 'X-GitHub-Delivery: kind-e2e-invalid' \
  -H 'X-Hub-Signature-256: sha256=0000000000000000000000000000000000000000000000000000000000000000' \
  --data "$invalid_webhook_payload" \
  "http://127.0.0.1:${controller_port}/webhooks/github/kind-success")"
[[ "$invalid_webhook_status" == "401" ]] || die "invalid webhook returned $invalid_webhook_status, want 401"
api_get "/api/v1/runs?limit=100" | jq -e '.runs | length == 0' >/dev/null

echo "kind-e2e: proving valid webhook, trigger claim, node execution, and web proxies"
IFS=$'\t' read -r initial_runner_pod initial_runner_uid < <(ready_pod_identity runner)
send_webhook kind-success
success_run=$webhook_run_id
wait_run_status "$success_run" success 300
success_nodes="$(api_get "/api/v1/runs/$success_run/nodes")"
jq -e '.nodes | length == 1 and .[0].status == "done" and (. [0].claimed_by | startswith("runner:"))' \
  <<<"$success_nodes" >/dev/null
success_claim="$(jq -er '.nodes[0].claimed_by' <<<"$success_nodes")"
success_started="$(jq -er '.nodes[0].started_at' <<<"$success_nodes")"
[[ "$success_claim" == "runner:${initial_runner_pod}:"* ]] || \
  die "initial run claim $success_claim does not identify Ready runner pod $initial_runner_pod ($initial_runner_uid)"
agents="$(api_get "/api/v1/agents")"
jq -e '.agents | any(.type == "agent")' <<<"$agents" >/dev/null

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
  grep -q "sparkwing-kind-e2e-success run_id=$success_run"

echo "kind-e2e: proving cancellation"
send_webhook kind-slow
cancelled_run=$webhook_run_id
wait_run_status "$cancelled_run" running 180
curl --fail --silent --show-error --max-time 10 \
  -X POST -H "Authorization: Bearer $admin_token" \
  "http://127.0.0.1:${controller_port}/api/v1/runs/$cancelled_run/cancel" >/dev/null
wait_run_status "$cancelled_run" cancelled 120

echo "kind-e2e: proving runner restart and a fresh claim"
runner_pod_before=$initial_runner_pod
runner_uid_before=$initial_runner_uid
kubectl --namespace "$namespace" rollout restart "deployment/$runner_deployment"
kubectl --namespace "$namespace" rollout status "deployment/$runner_deployment" --timeout=300s
kubectl --namespace "$namespace" get "deployment/$runner_deployment" -o json | \
  jq -e '.spec.replicas == 1 and .status.readyReplicas == 1 and .status.updatedReplicas == 1 and (.status.unavailableReplicas // 0) == 0' >/dev/null
IFS=$'\t' read -r runner_pod_after runner_uid_after < <(ready_pod_identity runner)
[[ "$runner_uid_after" != "$runner_uid_before" ]] || die "runner rollout did not replace its pod"
[[ "$runner_pod_after" != "$runner_pod_before" ]] || die "runner rollout did not replace its pod name"
send_webhook kind-success
post_runner_restart_run=$webhook_run_id
wait_run_status "$post_runner_restart_run" success 300
post_runner_nodes="$(api_get "/api/v1/runs/$post_runner_restart_run/nodes")"
post_runner_claim="$(jq -er '.nodes[0].claimed_by | select(startswith("runner:"))' <<<"$post_runner_nodes")"
[[ "$post_runner_claim" == "runner:${runner_pod_after}:"* ]] || \
  die "post-restart claim $post_runner_claim does not identify replacement runner pod $runner_pod_after"

echo "kind-e2e: proving controller restart and retained run state"
IFS=$'\t' read -r controller_pod_before controller_uid_before < <(ready_pod_identity controller)
kubectl --namespace "$namespace" rollout restart "deployment/$controller_deployment"
kubectl --namespace "$namespace" rollout status "deployment/$controller_deployment" --timeout=300s
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

echo "kind-e2e: proving retry"
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
  and (.[0].claimed_by | startswith("runner:"))
  and .[0].started_at != null
  and .[0].finished_at != null
  and .[0].started_at != $source_started
' <<<"$retry_nodes" >/dev/null
retry_output="$(api_get "/api/v1/runs/$retry_run/nodes/prove-controller-runner-logs/output" | jq -er '.')"
[[ "$retry_output" == "$retry_run" ]] || die "retry node output $retry_output does not match retry run $retry_run"
curl --fail --silent --show-error --max-time 10 \
  "http://127.0.0.1:${web_port}/api/v1/runs/$retry_run/logs/prove-controller-runner-logs" | \
  grep -q "sparkwing-kind-e2e-success run_id=$retry_run"

echo "kind-e2e: proving uninstall retention and reinstall recovery"
controller_pvc_uid="$(kubectl --namespace "$namespace" get pvc "$controller_pvc" -o jsonpath='{.metadata.uid}')"
stop_forwards
controller_port=""
web_port=""
helm uninstall "$release_name" --namespace "$namespace" --timeout 5m
release_installed=0
retained_pvc_uid="$(kubectl --namespace "$namespace" get pvc "$controller_pvc" -o jsonpath='{.metadata.uid}')"
[[ "$retained_pvc_uid" == "$controller_pvc_uid" ]] || die "controller PVC was not retained across uninstall"

helm install "$release_name" "$repo_root/charts/sparkwing-full" \
  --namespace "$namespace" -f "$bootstrap_values" -f "$authenticated_values" --timeout 5m --wait
release_installed=1
kubectl --namespace "$namespace" wait deployment \
  -l "app.kubernetes.io/instance=$release_name" \
  --for=condition=Available --timeout=300s
start_forward "$controller_service" 80 controller-reinstall
controller_port=$forward_port
start_forward "$web_service" 80 web-reinstall
web_port=$forward_port
api_get "/api/v1/runs/$success_run" | jq -e '.status == "success"' >/dev/null
curl --fail --silent --show-error --max-time 10 \
  "http://127.0.0.1:${web_port}/api/v1/runs/$success_run/logs/prove-controller-runner-logs" | \
  grep -q "sparkwing-kind-e2e-success run_id=$success_run"

cat >"$artifact_dir/result.txt" <<EOF
status=success
success_run=$success_run
cancelled_run=$cancelled_run
post_runner_restart_run=$post_runner_restart_run
retry_run=$retry_run
controller_pvc=$controller_pvc
fixture_sha=$fixture_sha
EOF
