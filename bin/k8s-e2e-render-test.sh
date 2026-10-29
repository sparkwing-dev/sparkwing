#!/usr/bin/env bash
set -euo pipefail
repo_root="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
real_helm="$(command -v helm)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/artifacts"
cat >"$work/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
cat >/dev/null
exit 0
STUB
cat >"$work/bin/helm" <<'STUB'
#!/usr/bin/env bash
[[ ${1:-} != --kube-context ]] || shift 2
case "$1" in
  version|lint|template) exec "$REAL_HELM" "$@" ;;
  install) touch "$INSTALL_REACHED"; exit 91 ;;
  *) exit 0 ;;
esac
STUB
chmod +x "$work/bin/helm" "$work/bin/kubectl"
printf 'offline render fixture\n' >"$work/license"
export SPARKWING_K8S_E2E_LICENSE_FILE="$work/license"
export REAL_HELM="$real_helm" INSTALL_REACHED="$work/install"
export PATH="$work/bin:$PATH"
export SPARKWING_K8S_E2E_KUBE_CONTEXT=offline-fixture
export SPARKWING_K8S_E2E_IMAGE_PREFIX=offline.invalid/test
export SPARKWING_K8S_E2E_TAG=fixture
export SPARKWING_K8S_E2E_ALLOW_CLEANUP=sparkwing-e2e/sparkwing
export SPARKWING_K8S_E2E_KEEP_RESOURCES=1
export SPARKWING_K8S_E2E_ARTIFACT_DIR="$work/artifacts"
bash "$repo_root/bin/k8s-e2e.sh" </dev/null >"$work/output" 2>&1 && exit 1
if [[ ! -f "$work/install" || ! -s "$work/artifacts/rendered-chart.yaml" ]]; then
  cat "$work/output" >&2
  exit 1
fi
"$real_helm" template sparkwing "$repo_root/charts/sparkwing-full" \
  --namespace sparkwing-e2e -f "$work/artifacts/values-bootstrap.yaml"
cat "$work/artifacts/rendered-chart.yaml"
