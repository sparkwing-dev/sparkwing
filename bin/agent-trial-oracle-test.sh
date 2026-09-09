#!/usr/bin/env bash
# Regression test for bin/agent-trial-oracle.sh. Stubs `sparkwing` so the
# oracle reads the NDJSON `pipeline list -o json` actually emits.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ORACLE="$ROOT/bin/agent-trial-oracle.sh"
CASE_ROOT="$(mktemp -d)"
trap 'rm -rf "$CASE_ROOT"' EXIT

mkdir -p "$CASE_ROOT/tools" "$CASE_ROOT/nojq" "$CASE_ROOT/trial/.sparkwing/jobs"
cat >"$CASE_ROOT/tools/sparkwing" <<'STUB'
#!/usr/bin/env bash
cat "$ORACLE_TEST_RECORDS"
STUB
chmod +x "$CASE_ROOT/tools/sparkwing"
export PATH="$CASE_ROOT/tools:$PATH"

cat >"$CASE_ROOT/records.ndjson" <<'NDJSON'
{"name":"ci","short":"Lint and test on pull requests","entrypoint":"CI"}
{"name":"release","short":"Tag and publish","entrypoint":"Release"}
NDJSON
export ORACLE_TEST_RECORDS="$CASE_ROOT/records.ndjson"

cat >"$CASE_ROOT/trial/.sparkwing/sparkwing.yaml" <<'YAML'
pipelines:
  - name: ci
    entrypoint: CI
    on:
      pull_request:
        branches: [main]
YAML
printf 'package jobs\n\n// go test ./...\n' >"$CASE_ROOT/trial/.sparkwing/jobs/ci.go"

failures=0
check() {
  local label="$1" want="$2" got="$3"
  if [[ "$got" != *"$want"* ]]; then
    echo "FAIL: $label"
    echo "  want substring: $want"
    echo "  got:            $got"
    failures=$((failures + 1))
  fi
}

listing=$(bash "$ORACLE" list "$CASE_ROOT/trial")
check "list names every record" "ci	Lint and test on pull requests" "$listing"
check "list names the second record" "release	Tag and publish" "$listing"
if [[ "$(printf '%s\n' "$listing" | grep -c .)" != "2" ]]; then
  echo "FAIL: list returned $(printf '%s\n' "$listing" | grep -c .) row(s) for 2 pipelines"
  failures=$((failures + 1))
fi

expect="$CASE_ROOT/pass.expect"
cat >"$expect" <<'EXPECT'
# comment ignored

pipelines 2
trigger pull_request
job go test
EXPECT
check "satisfied expectations pass" "task:    PASS (3 expectation(s))" \
  "$(bash "$ORACLE" task "$CASE_ROOT/trial" "$expect")"

expect="$CASE_ROOT/missing-trigger.expect"
printf 'trigger push\n' >"$expect"
check "an absent trigger fails" "missing: trigger push" \
  "$(bash "$ORACLE" task "$CASE_ROOT/trial" "$expect")"

expect="$CASE_ROOT/too-many.expect"
printf 'pipelines 3\n' >"$expect"
check "a shortfall in pipeline count fails" "missing: pipelines 3" \
  "$(bash "$ORACLE" task "$CASE_ROOT/trial" "$expect")"

check "a missing expect file is reported, not an error" "no absent.expect" \
  "$(bash "$ORACLE" task "$CASE_ROOT/trial" "$CASE_ROOT/absent.expect")"

for tool in env bash cat basename grep sparkwing; do
  ln -sf "$(command -v "$tool")" "$CASE_ROOT/nojq/$tool"
done
stripped=$(PATH="$CASE_ROOT/nojq" bash "$ORACLE" list "$CASE_ROOT/trial" 2>&1)
check "a missing jq is named, not silently green" "jq not found on PATH" "$stripped"

if [[ "$failures" -ne 0 ]]; then
  echo "agent-trial-oracle-test: $failures failure(s)"
  exit 1
fi
echo "agent-trial-oracle-test: ok"
