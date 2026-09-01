#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MODULE="$ROOT/install/terraform/mode3-postgres"
FIXTURE="$MODULE/test/plan"

if ! command -v terraform >/dev/null 2>&1; then
  echo "check-terraform: terraform not installed (see install/terraform/mode3-postgres/README.md)" >&2
  exit 1
fi

fail=0

echo "== fmt =="
terraform -chdir="$MODULE" fmt -check -recursive || { echo "fmt: run 'terraform fmt -recursive' in $MODULE"; fail=1; }

echo "== validate =="
terraform -chdir="$MODULE" init -backend=false -input=false >/dev/null
terraform -chdir="$MODULE" validate || fail=1

echo "== plan (offline, both engines) =="
terraform -chdir="$FIXTURE" init -backend=false -input=false >/dev/null

assert_plan() {
  local engine="$1"
  shift
  local plan addr
  if ! plan="$(terraform -chdir="$FIXTURE" plan -input=false -no-color -var "engine=$engine")"; then
    echo "plan engine=$engine: terraform plan failed"
    fail=1
    return
  fi
  for addr in "$@"; do
    case "$addr" in
      '!'*)
        if grep -qF "${addr#!} will be created" <<<"$plan"; then
          echo "plan engine=$engine: unexpected resource ${addr#!}"
          fail=1
        fi
        ;;
      *)
        if ! grep -qF "$addr will be created" <<<"$plan"; then
          echo "plan engine=$engine: missing resource $addr"
          fail=1
        fi
        ;;
    esac
  done
  echo "plan engine=$engine: resource set asserted"
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
if ! terraform -chdir="$MODULE" test; then
  echo "test: security attribute assertions failed (tests/security.tftest.hcl)"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  echo "check-terraform: FAILED" >&2
  exit 1
fi
echo "check-terraform: clean"
