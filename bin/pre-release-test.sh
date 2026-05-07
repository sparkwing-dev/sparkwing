#!/usr/bin/env bash
# wing: pre-release-test
# desc: Full pre-release test suite (unit, security, build, config)
# arg: quick (bool) Skip slow tests
#
# Pre-release test suite — run before all major releases.
# Covers: unit tests, security tests, build verification, config validation.
#
# Usage:
#   ./bin/pre-release-test.sh           # full suite
#   ./bin/pre-release-test.sh --quick   # skip slow tests
set -uo pipefail

CYAN="\033[36m"
GREEN="\033[32m"
RED="\033[31m"
YELLOW="\033[33m"
BOLD="\033[1m"
DIM="\033[2m"
RESET="\033[0m"

QUICK=false
if [[ "${1:-}" == "--quick" ]]; then
  QUICK=true
fi

PASS=0
FAIL=0
TOTAL_START=$SECONDS

run_check() {
  local name="$1"
  shift
  printf "  %-45s " "$name"
  START=$SECONDS
  if OUTPUT=$("$@" 2>&1); then
    DUR=$(( SECONDS - START ))
    echo -e "${GREEN}PASS${RESET} ${DIM}(${DUR}s)${RESET}"
    PASS=$((PASS + 1))
    return 0
  else
    DUR=$(( SECONDS - START ))
    echo -e "${RED}FAIL${RESET} ${DIM}(${DUR}s)${RESET}"
    echo "$OUTPUT" | tail -5 | while read -r l; do
      echo -e "    ${DIM}$l${RESET}"
    done
    FAIL=$((FAIL + 1))
    return 1
  fi
}

echo -e "${BOLD}Sparkwing Pre-Release Test Suite${RESET}\n"

# --- 1. Compilation ---
echo -e "${BOLD}1. Build Verification${RESET}"
run_check "Go build (all packages)" go build ./...
run_check "Go vet (all packages)" go vet ./...

# --- 2. Unit Tests ---
echo -e "\n${BOLD}2. Unit Tests${RESET}"
run_check "Controller tests" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -count=1"
run_check "CLI tests" go test ./internal/cli/ -count=1
run_check "Workflow engine tests" go test ./pkg/workflow/ -count=1
run_check "Step library tests" go test ./pkg/step/ -count=1
run_check "Artifact client tests" go test ./pkg/artifact/ -count=1
run_check "Git cache tests" go test ./cmd/sparkwing-gitcache/ -count=1

# --- 3. Security Tests ---
echo -e "\n${BOLD}3. Security Tests${RESET}"
run_check "Auth middleware" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestAuthMiddleware -count=1"
run_check "Webhook signature verification" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run 'TestWebhook|TestVerifySignature' -count=1"
run_check "Repo URL validation" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run 'TestValidRepo|TestExtractRepo' -count=1"
run_check "Git ref sanitization" go test ./cmd/sparkwing-gitcache/ -run TestValidateGitRef -count=1
run_check "Path traversal (docker)" go test ./pkg/step/ -run "TestDockerBuild_Subdir|TestDockerRun_Mount" -count=1
run_check "Path traversal (shell)" go test ./pkg/step/ -run TestScript_Path -count=1
run_check "Path traversal (artifact)" go test ./cmd/sparkwing-gitcache/ -run TestArtifactUpload_Directory -count=1
run_check "YAML injection" go test ./internal/cli/ -run TestYamlEscape -count=1
run_check "Env var name validation" go test ./internal/cli/ -run TestMasterRunnerManifest_Rejects -count=1
run_check "Log truncation" go test ./pkg/workflow/ -run TestLimitedWriter -count=1
run_check "Rate limiter + XFF" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run 'TestRateLimit|TestClientIP' -count=1"
run_check "Secrets encryption" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestSecretStore -count=1"
run_check "Log masking" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestMaskSecrets -count=1"

# --- 4. Trigger Matching ---
echo -e "\n${BOLD}4. Trigger & Config Tests${RESET}"
run_check "Push trigger matching" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestMatchesPush -count=1"
run_check "PR trigger matching" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestMatchesPR -count=1"
run_check "Multi-app workflow matching" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestMatchPushWorkflows -count=1"
run_check "Concurrency cancel-in-progress" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestCancelOthersInGroup -count=1"
run_check "Timeout enforcement" bash -c "SPARKWING_DISABLE_FLAKY=1 go test ./internal/controller/ -run TestCancelTimedOutJobs -count=1"
run_check "Config matching (CLI)" go test ./internal/cli/ -run "TestMatches|TestEffective" -count=1
run_check "Path change detection" go test ./pkg/step/ -run "TestPathsMatch|TestGlobStar|TestWhenPaths" -count=1

# --- 5. Step Library ---
echo -e "\n${BOLD}5. Step Library Tests${RESET}"
run_check "Retry with backoff" go test ./pkg/step/ -run TestRetry -count=1
run_check "JUnit XML parsing" go test ./pkg/step/ -run TestParseJUnit -count=1
run_check "Spawn context cancellation" go test ./pkg/step/ -run TestSpawn -count=1

# --- 6. Slow tests (skipped in --quick mode) ---
if [[ "$QUICK" == false ]]; then
  echo -e "\n${BOLD}6. Integration Tests${RESET}"
  if [[ -x ./bin/security-test.sh ]]; then
    run_check "Security integration tests" ./bin/security-test.sh
  else
    echo -e "  ${YELLOW}SKIP${RESET}  security-test.sh not found"
  fi
  if [[ -x ./bin/e2e-test.sh ]]; then
    run_check "E2E tests" ./bin/e2e-test.sh
  else
    echo -e "  ${YELLOW}SKIP${RESET}  e2e-test.sh not found"
  fi
else
  echo -e "\n${DIM}Skipping integration tests (--quick mode)${RESET}"
fi

# --- Summary ---
TOTAL_DUR=$(( SECONDS - TOTAL_START ))
TOTAL=$((PASS + FAIL))

echo -e "\n${BOLD}Results${RESET}"
echo -e "  ${GREEN}${PASS} passed${RESET}  ${RED}${FAIL} failed${RESET}  ${DIM}${TOTAL} checks  ${TOTAL_DUR}s${RESET}"

if [[ $FAIL -eq 0 ]]; then
  echo -e "\n${GREEN}${BOLD}Pre-release checks passed. Ready to release.${RESET}"
  exit 0
else
  echo -e "\n${RED}${BOLD}${FAIL} check(s) failed. Fix before release.${RESET}"
  exit 1
fi
