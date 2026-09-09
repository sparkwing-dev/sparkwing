#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "$0")/.." && pwd)"
module="$repository_root/install/terraform/mode3-postgres"
plan_fixture="$module/test/plan"

if ! command -v terraform >/dev/null 2>&1; then
  echo "check-terraform: terraform not installed (see install/terraform/mode3-postgres/README.md)" >&2
  exit 1
fi

data_root="$(mktemp -d "${TMPDIR:-/tmp}/sparkwing-terraform.XXXXXX")"
trap 'rm -rf "$data_root"' EXIT
module_data="$data_root/module"
fixture_data="$data_root/fixture"
mkdir -p "$module_data" "$fixture_data"

fail=0

echo "== fmt =="
TF_DATA_DIR="$module_data" terraform -chdir="$module" fmt -check -recursive || { echo "fmt: run 'terraform fmt -recursive' in $module"; fail=1; }

echo "== validate =="
TF_DATA_DIR="$module_data" terraform -chdir="$module" init -backend=false -input=false >/dev/null
TF_DATA_DIR="$module_data" terraform -chdir="$module" validate || fail=1

echo "== plan (both engines) =="
TF_DATA_DIR="$fixture_data" terraform -chdir="$plan_fixture" init -backend=false -input=false >/dev/null

assert_plan() {
  local engine="$1"
  shift
  local plan resource_address
  if ! plan="$(TF_DATA_DIR="$fixture_data" terraform -chdir="$plan_fixture" plan -input=false -no-color -var "engine=$engine")"; then
    echo "plan engine=$engine: terraform plan failed"
    fail=1
    return
  fi
  for resource_address in "$@"; do
    case "$resource_address" in
      '!'*)
        if grep -qF "${resource_address#!} will be created" <<<"$plan"; then
          echo "plan engine=$engine: unexpected resource ${resource_address#!}"
          fail=1
        fi
        ;;
      *)
        if ! grep -qF "$resource_address will be created" <<<"$plan"; then
          echo "plan engine=$engine: missing resource $resource_address"
          fail=1
        fi
        ;;
    esac
  done
  echo "plan engine=$engine: expected resources found"
}

common=(
  module.db.random_password.master
  module.db.aws_db_subnet_group.this
  module.db.aws_security_group.this
  module.db.aws_secretsmanager_secret.dsn
  module.db.aws_secretsmanager_secret_version.dsn
)

assert_plan rds "${common[@]}" \
  "module.db.aws_db_instance.this[0]" \
  "!module.db.aws_rds_cluster.this[0]" \
  "!module.db.aws_rds_cluster_instance.this[0]"

assert_plan aurora-serverless-v2 "${common[@]}" \
  "module.db.aws_rds_cluster.this[0]" \
  "module.db.aws_rds_cluster_instance.this[0]" \
  "!module.db.aws_db_instance.this[0]"

echo "== test (security attribute values, offline) =="
if ! TF_DATA_DIR="$module_data" terraform -chdir="$module" test; then
  echo "test: security attribute assertions failed"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "check-terraform: FAILED" >&2
  exit 1
fi
echo "check-terraform: passed"
