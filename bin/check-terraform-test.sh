#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/fictional-terraform-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT
mkdir -p "$fixture/tools" "$fixture/data" "$fixture/repository/bin"
cp "$root/bin/check-terraform.sh" "$fixture/repository/bin/"
cat >"$fixture/tools/terraform" <<'TOOL'
#!/usr/bin/env bash
set -euo pipefail
module="${1#-chdir=}"
shift
data="${TF_DATA_DIR:-$module/.terraform}"
printf '%s\t%s\n' "$data" "$*" >>"$CHECK_TERRAFORM_CAPTURE"
case "$1" in
  init)
    mkdir -p "$data"
    if [[ "${CHECK_TERRAFORM_FAIL:-}" == 1 ]]; then
      exit 17
    fi
    if ! mkdir "$data/fictional-writer" 2>/dev/null; then
      echo "concurrent initialization shares $data" >&2
      exit 18
    fi
    sleep 0.2
    rmdir "$data/fictional-writer"
    ;;
  plan)
    cat <<'PLAN'
module.db.random_password.master will be created
module.db.aws_db_subnet_group.this will be created
module.db.aws_security_group.this will be created
module.db.aws_secretsmanager_secret.dsn will be created
module.db.aws_secretsmanager_secret_version.dsn will be created
PLAN
    case " $* " in
      *" engine=rds "*) echo 'module.db.aws_db_instance.this[0] will be created' ;;
      *)
        echo 'module.db.aws_rds_cluster.this[0] will be created'
        echo 'module.db.aws_rds_cluster_instance.this[0] will be created'
        ;;
    esac
    ;;
esac
TOOL
chmod +x "$fixture/tools/terraform"

run_check() {
  CHECK_TERRAFORM_CAPTURE="$fixture/capture-$1" \
    CHECK_TERRAFORM_FAIL="${2:-0}" \
    TMPDIR="$fixture/data" \
    PATH="$fixture/tools:$PATH" \
    bash "$fixture/repository/bin/check-terraform.sh" >"$fixture/output-$1" 2>&1
}

run_check first &
first_pid=$!
run_check second &
second_pid=$!
failed=0
wait "$first_pid" || failed=1
wait "$second_pid" || failed=1
if [[ "$failed" != 0 ]]; then
  cat "$fixture/output-first" "$fixture/output-second" >&2
  echo "check-terraform-test: concurrent checks failed" >&2
  exit 1
fi

data_dirs=()
while IFS= read -r data_dir; do
  data_dirs+=("$data_dir")
done < <(cat "$fixture/capture-first" "$fixture/capture-second" | cut -f1 | sort -u)
if [[ ${#data_dirs[@]} -ne 4 ]]; then
  echo "check-terraform-test: expected separate module and fixture data for each invocation" >&2
  exit 1
fi
for data_dir in "${data_dirs[@]}"; do
  case "$data_dir" in
    "$fixture/data"/*) ;;
    *) echo "check-terraform-test: data directory escaped private root: $data_dir" >&2; exit 1 ;;
  esac
  if [[ -e "$data_dir" ]]; then
    echo "check-terraform-test: data directory survived successful check: $data_dir" >&2
    exit 1
  fi
done

if run_check failure 1; then
  echo "check-terraform-test: initialization failure was hidden" >&2
  exit 1
fi
if [[ -n "$(find "$fixture/data" -mindepth 1 -print -quit)" ]]; then
  echo "check-terraform-test: data survived failed initialization" >&2
  exit 1
fi
