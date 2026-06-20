#!/usr/bin/env bash
# Validate and plan the Mode 3 Postgres Terraform module offline.
#
# Runs fmt + validate on the module, then plans the plan-only fixture
# under test/plan for both engine knobs (rds, aurora-serverless-v2) and
# asserts the expected resource set per knob: the networking wiring
# (security group + subnet group) is always present, and the count-gated
# engine resources flip correctly (single RDS instance vs Aurora cluster
# + instance). The fixture uses mock AWS credentials and every provider
# skip flag, so plan runs with no AWS account and reaches no API.
#
# Exit 0 if clean; non-zero with detail on any failure. Requires
# terraform on PATH (see install/terraform/mode3-postgres/README.md).
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

if [[ "$fail" -ne 0 ]]; then
  echo "check-terraform: FAILED" >&2
  exit 1
fi
echo "check-terraform: clean"
