#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fixture="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-terraform-test.XXXXXX")"
trap 'rm -rf "$fixture"' EXIT

mkdir -p "$fixture/tools" "$fixture/data"
capture="$fixture/capture"
cat >"$fixture/tools/terraform" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\t%s\n' "${TF_DATA_DIR:-}" "$*" >>"$CHECK_TERRAFORM_CAPTURE"
case " $* " in
  *" plan "*)
    cat <<'PLAN'
module.db.random_password.master will be created
module.db.aws_db_subnet_group.this will be created
module.db.aws_security_group.this will be created
module.db.aws_secretsmanager_secret.dsn will be created
module.db.aws_secretsmanager_secret_version.dsn will be created
PLAN
    case " $* " in
      *" engine=rds "*)
        echo 'module.db.aws_db_instance.this[0] will be created'
        ;;
      *)
        echo 'module.db.aws_rds_cluster.this[0] will be created'
        echo 'module.db.aws_rds_cluster_instance.this[0] will be created'
        ;;
    esac
    ;;
esac
EOF
chmod +x "$fixture/tools/terraform"

CHECK_TERRAFORM_CAPTURE="$capture" \
  TMPDIR="$fixture/data" \
  PATH="$fixture/tools:$PATH" \
  bash "$root/bin/check-terraform.sh" >/dev/null

if grep -q $'^\t' "$capture"; then
  echo "check-terraform-test: command ran without an isolated TF_DATA_DIR" >&2
  exit 1
fi

data_dirs=()
while IFS= read -r data_dir; do
  data_dirs+=("$data_dir")
done < <(cut -f1 "$capture" | sort -u)
if [[ ${#data_dirs[@]} -ne 2 ]]; then
  echo "check-terraform-test: got ${#data_dirs[@]} data directories, want module and fixture isolation" >&2
  exit 1
fi
for data_dir in "${data_dirs[@]}"; do
  case "$data_dir" in
    "$fixture/data"/*) ;;
    *)
      echo "check-terraform-test: data directory escaped private root: $data_dir" >&2
      exit 1
      ;;
  esac
  if [[ -e "$data_dir" ]]; then
    echo "check-terraform-test: data directory survived cleanup: $data_dir" >&2
    exit 1
  fi
done
