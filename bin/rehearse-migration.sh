#!/usr/bin/env bash
# Rehearse the store migration a release carries on copies of real data.
#
# For each copy it takes a baseline, plants canary credentials (a token, a
# password user, and plaintext, enc:v1 and enc:v2 secrets), starts the new
# controller on it the way an upgrade does, and checks the result: schema
# version, every baseline row still present, every team column 'default',
# the canaries still authenticate and decrypt, secrets resealed to enc:v3.
# Then it restarts the new controller (idempotency), starts it with the wrong
# secrets key (must refuse), and starts the old controller (the rollback
# story). Both CLI builds run the verify-the-install checklist against the new
# controller, and the new CLI against the old one, since an operator upgrades
# the two in either order. For SQLite it also migrates a fresh copy through the
# local CLI.
#
# The originals are only read: the SQLite store through VACUUM INTO, the
# Postgres archive through pg_restore into databases named rehearsal_pg_*,
# which each run drops and recreates, so run one rehearsal per server at a
# time. Everything else lives under --work, including copies of the data and
# the canary credentials; delete it when done.
#
# Usage:
#   bin/rehearse-migration.sh --work DIR [--sqlite STATE_DB] [--pg-dump FILE]
#       [--new-ref REF] [--old-ref REF] [--pg-admin-url URL] [--pg-bin DIR]
#
#   --work          scratch directory (created mode 700); rerunning with the
#                   same directory starts over
#   --sqlite        live SQLite store to snapshot (e.g. ~/.sparkwing/state.db)
#   --pg-dump       pg_dump --format=custom archive of a controller database
#   --new-ref       git ref of the release under test (default origin/tenancy)
#   --old-ref       git ref of the release being upgraded from (default origin/main)
#   --pg-admin-url  superuser URL on a scratch server; databases are created
#                   beside the one it names
#                   (default postgres://postgres:postgres@127.0.0.1:5433/postgres?sslmode=disable)
#   --pg-bin        directory holding psql and pg_restore (default $SPARKWING_PG_BIN or PATH)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK=""
SQLITE_SRC=""
PG_DUMP=""
NEW_REF="origin/tenancy"
OLD_REF="origin/main"
PG_ADMIN_URL="postgres://postgres:postgres@127.0.0.1:5433/postgres?sslmode=disable"
PG_BIN="${SPARKWING_PG_BIN:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --work) WORK="$2"; shift 2 ;;
    --sqlite) SQLITE_SRC="$2"; shift 2 ;;
    --pg-dump) PG_DUMP="$2"; shift 2 ;;
    --new-ref) NEW_REF="$2"; shift 2 ;;
    --old-ref) OLD_REF="$2"; shift 2 ;;
    --pg-admin-url) PG_ADMIN_URL="$2"; shift 2 ;;
    --pg-bin) PG_BIN="$2"; shift 2 ;;
    -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit 0 ;;
    *) echo "rehearse-migration: unknown argument $1" >&2; exit 2 ;;
  esac
done
if [[ -z "$WORK" ]]; then
  echo "rehearse-migration: --work is required" >&2
  exit 2
fi
if [[ -z "$SQLITE_SRC" && -z "$PG_DUMP" ]]; then
  echo "rehearse-migration: name at least one copy with --sqlite or --pg-dump" >&2
  exit 2
fi

PSQL=psql
PG_RESTORE=pg_restore
if [[ -n "$PG_BIN" ]]; then
  PSQL="$PG_BIN/psql"
  PG_RESTORE="$PG_BIN/pg_restore"
fi

export GOWORK=off
install -d -m 700 "$WORK"
WORK="$(cd "$WORK" && pwd)"
LOG="$WORK/rehearsal.log"
: > "$LOG"
FAILURES=0
CONTROLLER_PID=""

say() { printf '%s\n' "$*" | tee -a "$LOG"; }
fail() { FAILURES=$((FAILURES + 1)); say "FAIL  $*"; }
pass() { say "PASS  $*"; }

cleanup() {
  if [[ -n "$CONTROLLER_PID" ]] && kill -0 "$CONTROLLER_PID" 2>/dev/null; then
    kill "$CONTROLLER_PID" 2>/dev/null || true
    wait "$CONTROLLER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# safety: each binary is built from a git archive of its ref, never from the
# working tree, so an uncommitted edit cannot stand in for the release.
build_ref() {
  local ref="$1" name="$2" rev src version
  rev="$(git -C "$REPO_ROOT" rev-parse --verify "$ref^{commit}")"
  version="$(git -C "$REPO_ROOT" describe --tags "$rev" 2>/dev/null || printf 'v0.0.0-%s' "${rev:0:12}")"
  src="$WORK/src/$name"
  if [[ -f "$WORK/bin/$name/.rev" && "$(cat "$WORK/bin/$name/.rev")" == "$rev" ]]; then
    say "reuse $name binaries for $ref ($rev)"
    return
  fi
  install -d "$src" "$WORK/bin/$name"
  git -C "$REPO_ROOT" archive "$rev" | tar -x -C "$src"
  go -C "$src" build -trimpath -ldflags "-X main.Version=$version" -o "$WORK/bin/$name/" ./cmd/sparkwing-controller ./cmd/sparkwing
  printf '%s' "$rev" > "$WORK/bin/$name/.rev"
  say "built $name $version from $ref ($rev), schema $(grep -o 'expectedSchemaVersion = [0-9]*' "$src/pkg/store/store.go" | grep -o '[0-9]*$')"
}

TOOL="$WORK/bin/migrationrehearsal"
go -C "$REPO_ROOT" build -o "$TOOL" ./internal/migrationrehearsal
build_ref "$NEW_REF" new
build_ref "$OLD_REF" old
WANT_VERSION="$(grep -o 'expectedSchemaVersion = [0-9]*' "$WORK/src/new/pkg/store/store.go" | grep -o '[0-9]*$')"
NEW_CONTROLLER="$WORK/bin/new/sparkwing-controller"
OLD_CONTROLLER="$WORK/bin/old/sparkwing-controller"

elapsed() { awk -v a="$1" -v b="$(date +%s.%N)" 'BEGIN { printf "%.2f", b - a }'; }

free_port() {
  python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'
}

# Controllers run with a private HOME and no inherited SPARKWING_* variables,
# so nothing from the operator's own profile, daemon or store leaks in.
controller_env() {
  local home="$1"
  shift
  env -i PATH="$PATH" HOME="$home" SPARKWING_HOME="$home/.sparkwing" GOMAXPROCS="${GOMAXPROCS:-4}" "$@"
}

# start_controller <label> <binary> <home> <pg-url or ''> <secrets key or ''>
# Starts in the background and waits for /api/v1/health. Sets CONTROLLER_PID,
# CONTROLLER_URL and STARTUP_SECONDS; returns 1 when the process exits first.
start_controller() {
  local label="$1" bin="$2" home="$3" pgurl="$4" key="$5" port start
  port="$(free_port)"
  CONTROLLER_URL="http://127.0.0.1:$port"
  install -d -m 700 "$home/.sparkwing"
  local -a extra=()
  [[ -n "$pgurl" ]] && extra+=("SPARKWING_PG_URL=$pgurl")
  [[ -n "$key" ]] && extra+=("SPARKWING_SECRETS_KEY=$key")
  start="$(date +%s.%N)"
  START_NS="$(date +%s%N)"
  # safety: env is started directly rather than through controller_env, so $!
  # is the controller itself (env execs it) and not a subshell a signal would
  # stop while the controller kept running.
  env -i PATH="$PATH" HOME="$home" SPARKWING_HOME="$home/.sparkwing" GOMAXPROCS="${GOMAXPROCS:-4}" "${extra[@]}" \
    "$bin" --addr "127.0.0.1:$port" > "$WORK/$label.log" 2>&1 &
  CONTROLLER_PID=$!
  for _ in $(seq 1 1200); do
    if ! kill -0 "$CONTROLLER_PID" 2>/dev/null; then
      wait "$CONTROLLER_PID" 2>/dev/null && CONTROLLER_EXIT=0 || CONTROLLER_EXIT=$?
      CONTROLLER_PID=""
      STARTUP_SECONDS="$(elapsed "$start")"
      return 1
    fi
    if curl -fsS -o /dev/null "$CONTROLLER_URL/api/v1/health" 2>/dev/null; then
      STARTUP_SECONDS="$(elapsed "$start")"
      return 0
    fi
    sleep 0.1
  done
  fail "$label: controller neither answered nor exited within 120s"
  return 1
}

stop_controller() {
  if [[ -n "$CONTROLLER_PID" ]]; then
    kill -TERM "$CONTROLLER_PID" 2>/dev/null || true
    wait "$CONTROLLER_PID" 2>/dev/null || true
    if kill -0 "$CONTROLLER_PID" 2>/dev/null; then
      fail "controller $CONTROLLER_PID outlived SIGTERM"
      kill -KILL "$CONTROLLER_PID" 2>/dev/null || true
    fi
    CONTROLLER_PID=""
  fi
}

# Reports how long each migration step took from the applied_at stamps, the
# first measured from the controller's exec.
step_timings() {
  jq -r --argjson from "$2" --argjson start "$3" '
    [.versions[] | select(.version > $from)] as $v
    | if ($v | length) == 0 then "      no versions applied"
      else ($v | to_entries[] | "      v\(.value.version) committed \(((.value.applied_at - (if .key == 0 then $start else $v[.key-1].applied_at end)) / 1e6) | floor) ms after \(if .key == 0 then "exec" else "v\($v[.key-1].version)" end)")
      end' "$1"
}

# cli_verify <label> <sparkwing binary> <controller url> <creds dir> <run id>
# Runs the checklist from docs/backup-restore.md#verify-the-install with one
# CLI build against a running controller, the way an operator would. It reads
# the enc:v1 canary because a keyed controller from before the reseal answers
# 500 for a plaintext row.
cli_verify() {
  local label="$1" cli="$2" url="$3" creds="$4" run="$5" home="$WORK/cli/${1//[^a-zA-Z0-9]/_}" failed=0
  install -d -m 700 "$home"
  rm -f -- "$home/.config/sparkwing/profiles.yaml"
  controller_env "$home" "$cli" configure profiles add --name rehearsal --controller "$url" --token-stdin \
    < "$creds/canary-token" > "$home/add.log" 2>&1 || { fail "$label: profiles add: $(tail -1 "$home/add.log")"; return; }
  local -a checks=(
    "configure profiles test --profile rehearsal"
    "runs list --profile rehearsal"
    "runs get --run $run --profile rehearsal"
    "secrets list --profile rehearsal"
    "secrets get --profile rehearsal --name REHEARSAL_V1"
    "cluster credits show --profile rehearsal"
    "cluster credits history --profile rehearsal"
    "cluster tokens list --profile rehearsal"
    "cluster agents list --profile rehearsal"
    "cluster users list --profile rehearsal"
  )
  local c i=0
  for c in "${checks[@]}"; do
    i=$((i + 1))
    # shellcheck disable=SC2086 # each check is a word list by construction
    if ! controller_env "$home" "$cli" $c > "$home/$i.out" 2> "$home/$i.err"; then
      failed=$((failed + 1))
      fail "$label: sparkwing $c: $(tail -2 "$home/$i.err" | tr '\n' ' ')"
    fi
  done
  if [[ "$failed" -eq 0 ]]; then
    pass "$label: all ${#checks[@]} verify-the-install commands succeed"
  fi
}

run_tool() {
  "$TOOL" "$@" 2>&1 | tee -a "$LOG"
  return "${PIPESTATUS[0]}"
}

# rehearse <dialect> <label> <migrate-dsn> <baseline-dsn> <after1-dsn> <home>
#          <pg-url or ''> <copy-to-baseline command> <copy-to-after1 command>
rehearse() {
  local dialect="$1" label="$2" dsn="$3" baseline="$4" after1="$5" home="$6" pgurl="$7"
  local snap_baseline="$8" snap_after1="$9"
  local creds="$WORK/$label/creds" key from start_ns
  say ""
  say "== $label: plant canaries and take the baseline"
  run_tool canary -dialect "$dialect" -dsn "$dsn" -out "$creds" || { fail "$label: canary"; return; }
  key="$(cat "$creds/secrets-key")"
  eval "$snap_baseline"
  "$TOOL" inventory -dialect "$dialect" -dsn "$baseline" > "$WORK/$label/before.json"
  from="$(jq .schema_version "$WORK/$label/before.json")"
  say "baseline: schema $from, $(jq -r '"runs \(.runs), nodes \(.nodes), tokens \(.tokens) (incl. canary), secrets \(.secrets) (incl. 3 canaries), users \(.users) (incl. canary), last run \(.last_run.id // "none")"' "$WORK/$label/before.json")"

  say ""
  say "== $label: first start of the new controller (migrates $from -> $WANT_VERSION, secrets key set)"
  if start_controller "$label-start1" "$NEW_CONTROLLER" "$home" "$pgurl" "$key"; then
    pass "$label: new controller answered health ${STARTUP_SECONDS}s after exec (migration included)"
    grep -m1 'runs-store schema' "$WORK/$label-start1.log" | tee -a "$LOG" || true
    grep 'stored secrets resealed' "$WORK/$label-start1.log" | tee -a "$LOG" || true
    start_ns="$START_NS"
    run_tool api-check -url "$CONTROLLER_URL" -creds "$creds" -inventory "$WORK/$label/before.json" -keyed || fail "$label: api-check after the first start"
    stop_controller
  else
    fail "$label: new controller exited ($CONTROLLER_EXIT) on first start after ${STARTUP_SECONDS}s"
    tail -20 "$WORK/$label-start1.log" | tee -a "$LOG"
    return
  fi
  "$TOOL" inventory -dialect "$dialect" -dsn "$dsn" > "$WORK/$label/after1.json"
  step_timings "$WORK/$label/after1.json" "$from" "$start_ns" | tee -a "$LOG"
  say ""
  say "== $label: compare baseline with the migrated copy"
  run_tool compare -dialect "$dialect" -before "$baseline" -after "$dsn" -want-version "$WANT_VERSION" || fail "$label: compare"
  if [[ "$(jq -c '.secret_envelopes | keys' "$WORK/$label/after1.json")" == '["enc:v3"]' ]]; then
    pass "$label: every secret is an enc:v3 envelope"
  else
    fail "$label: secret envelopes after a keyed start: $(jq -c .secret_envelopes "$WORK/$label/after1.json")"
  fi

  say ""
  say "== $label: second start (idempotency)"
  eval "$snap_after1"
  if start_controller "$label-start2" "$NEW_CONTROLLER" "$home" "$pgurl" "$key"; then
    pass "$label: second start answered in ${STARTUP_SECONDS}s"
    grep 'stored secrets resealed' "$WORK/$label-start2.log" | tee -a "$LOG" || true
    stop_controller
  else
    fail "$label: second start exited ($CONTROLLER_EXIT)"
    tail -20 "$WORK/$label-start2.log" | tee -a "$LOG"
  fi
  "$TOOL" inventory -dialect "$dialect" -dsn "$dsn" > "$WORK/$label/after2.json"
  if [[ "$(jq -c .versions "$WORK/$label/after1.json")" == "$(jq -c .versions "$WORK/$label/after2.json")" ]]; then
    pass "$label: second start applied no schema step"
  else
    fail "$label: second start changed sparkwing_schema_version"
  fi
  run_tool compare -dialect "$dialect" -before "$after1" -after "$dsn" -want-version "$WANT_VERSION" || fail "$label: compare after the second start"

  say ""
  say "== $label: both CLI builds against the new controller"
  if start_controller "$label-cli" "$NEW_CONTROLLER" "$home" "$pgurl" "$key"; then
    cli_verify "$label: new CLI -> new controller" "$WORK/bin/new/sparkwing" "$CONTROLLER_URL" "$creds" "$(jq -r .last_run.id "$WORK/$label/before.json")"
    cli_verify "$label: old CLI -> new controller" "$WORK/bin/old/sparkwing" "$CONTROLLER_URL" "$creds" "$(jq -r .last_run.id "$WORK/$label/before.json")"
    stop_controller
  else
    fail "$label: new controller exited ($CONTROLLER_EXIT) before the CLI checks"
  fi

  say ""
  say "== $label: wrong secrets key (must refuse)"
  local wrong
  wrong="$(head -c 32 /dev/urandom | base64)"
  if start_controller "$label-wrongkey" "$NEW_CONTROLLER" "$home" "$pgurl" "$wrong"; then
    fail "$label: the controller started under a key that opens none of the stored secrets"
    stop_controller
  else
    pass "$label: wrong key refused (exit $CONTROLLER_EXIT): $(grep -m1 -i 'secrets key' "$WORK/$label-wrongkey.log" || tail -1 "$WORK/$label-wrongkey.log")"
  fi

  say ""
  say "== $label: rollback, the old controller on the migrated copy"
  if start_controller "$label-rollback" "$OLD_CONTROLLER" "$home" "$pgurl" "$key"; then
    fail "$label: the old controller started on the migrated store"
    stop_controller
  else
    pass "$label: old controller refused (exit $CONTROLLER_EXIT)"
    sed 's/^/      /' "$WORK/$label-rollback.log" | tail -6 | tee -a "$LOG"
  fi
  "$TOOL" inventory -dialect "$dialect" -dsn "$dsn" > "$WORK/$label/after-rollback.json"
  if [[ "$(jq -c .versions "$WORK/$label/after2.json")" == "$(jq -c .versions "$WORK/$label/after-rollback.json")" ]]; then
    pass "$label: the refused old controller left the schema as it was"
  else
    fail "$label: the old controller changed sparkwing_schema_version before refusing"
  fi
}

if [[ -n "$SQLITE_SRC" ]]; then
  S="$WORK/sqlite"
  install -d -m 700 "$S"
  for f in original.db baseline.db after1.db cli.db home/.sparkwing/state.db home/.sparkwing/state.db-wal home/.sparkwing/state.db-shm; do
    [[ -e "$S/$f" ]] && rm -f -- "$S/$f"
  done
  say "== sqlite: snapshot $SQLITE_SRC"
  run_tool snapshot -from "$SQLITE_SRC" -to "$S/original.db"
  "$TOOL" inventory -dialect sqlite -dsn "$S/original.db" > "$S/original.json"
  install -d -m 700 "$S/home/.sparkwing"
  cp "$S/original.db" "$S/home/.sparkwing/state.db"
  rehearse sqlite sqlite "$S/home/.sparkwing/state.db" "$S/baseline.db" "$S/after1.db" "$S/home" "" \
    "\"\$TOOL\" snapshot -from \"$S/home/.sparkwing/state.db\" -to \"$S/baseline.db\" >> \"\$LOG\"" \
    "\"\$TOOL\" snapshot -from \"$S/home/.sparkwing/state.db\" -to \"$S/after1.db\" >> \"\$LOG\""

  say ""
  say "== sqlite: new CLI against the old controller (CLI upgraded first)"
  install -d -m 700 "$S/oldctl-home/.sparkwing"
  rm -f -- "$S/oldctl-home/.sparkwing/state.db" "$S/oldctl-home/.sparkwing/state.db-wal" "$S/oldctl-home/.sparkwing/state.db-shm"
  cp "$S/baseline.db" "$S/oldctl-home/.sparkwing/state.db"
  if start_controller sqlite-oldctl "$OLD_CONTROLLER" "$S/oldctl-home" "" "$(cat "$WORK/sqlite/creds/secrets-key")"; then
    cli_verify "sqlite: new CLI -> old controller" "$WORK/bin/new/sparkwing" "$CONTROLLER_URL" "$WORK/sqlite/creds" "$(jq -r .last_run.id "$WORK/sqlite/before.json")"
    stop_controller
  else
    fail "sqlite: the old controller did not start on the baseline: $(tail -2 "$WORK/sqlite-oldctl.log")"
  fi

  say ""
  say "== sqlite: the local CLI path on a fresh copy"
  install -d -m 700 "$S/cli-home/.sparkwing"
  rm -f -- "$S/cli-home/.sparkwing/state.db" "$S/cli-home/.sparkwing/state.db-wal" "$S/cli-home/.sparkwing/state.db-shm"
  cp "$S/original.db" "$S/cli-home/.sparkwing/state.db"
  start="$(date +%s.%N)"
  if controller_env "$S/cli-home" "$WORK/bin/new/sparkwing" runs list -o json > "$S/cli-runs.json" 2> "$S/cli-runs.err"; then
    pass "sqlite: new CLI 'runs list' migrated and read the store in $(elapsed "$start")s, $(wc -l < "$S/cli-runs.json") runs listed"
  else
    fail "sqlite: new CLI 'runs list' failed: $(tail -3 "$S/cli-runs.err")"
  fi
  "$TOOL" inventory -dialect sqlite -dsn "$S/cli-home/.sparkwing/state.db" > "$S/cli-after.json"
  say "      CLI copy now at schema $(jq .schema_version "$S/cli-after.json")"
  run_tool compare -dialect sqlite -before "$S/original.db" -after "$S/cli-home/.sparkwing/state.db" -want-version "$WANT_VERSION" || fail "sqlite: compare after the CLI migration"
  if controller_env "$S/cli-home" "$WORK/bin/old/sparkwing" runs list -o json > /dev/null 2> "$S/cli-old.err"; then
    fail "sqlite: the old CLI read the migrated store"
  else
    pass "sqlite: old CLI refused the migrated store"
    sed 's/^/      /' "$S/cli-old.err" | tail -4 | tee -a "$LOG"
  fi
fi

if [[ -n "$PG_DUMP" ]]; then
  base_url="${PG_ADMIN_URL%/*}"
  query="${PG_ADMIN_URL#*\?}"
  [[ "$query" == "$PG_ADMIN_URL" ]] && query="" || query="?$query"
  pg_url() { printf '%s/%s%s' "$base_url" "$1" "$query"; }
  admin() { "$PSQL" "$PG_ADMIN_URL" -v ON_ERROR_STOP=1 -qAtc "$1" >> "$LOG"; }
  # safety: a stopped controller's backends outlive its process by a moment,
  # and CREATE DATABASE refuses a template anyone is still connected to.
  pg_copy() {
    for _ in $(seq 1 50); do
      if admin "CREATE DATABASE $2 TEMPLATE $1" 2>/dev/null; then
        return 0
      fi
      sleep 0.2
    done
    admin "CREATE DATABASE $2 TEMPLATE $1"
  }
  for db in rehearsal_pg_original rehearsal_pg_migrate rehearsal_pg_baseline rehearsal_pg_after1 rehearsal_pg_oldctl; do
    admin "DROP DATABASE IF EXISTS $db"
  done
  say ""
  say "== postgres: restore $PG_DUMP ($(stat -c %s "$PG_DUMP") bytes)"
  admin "CREATE DATABASE rehearsal_pg_original"
  "$PG_RESTORE" --no-owner --no-privileges --exit-on-error --dbname="$(pg_url rehearsal_pg_original)" "$PG_DUMP"
  "$TOOL" inventory -dialect postgres -dsn "$(pg_url rehearsal_pg_original)" > "$WORK/pg-original.json"
  pg_copy rehearsal_pg_original rehearsal_pg_migrate
  install -d -m 700 "$WORK/pg/home"
  rehearse postgres pg "$(pg_url rehearsal_pg_migrate)" "$(pg_url rehearsal_pg_baseline)" "$(pg_url rehearsal_pg_after1)" \
    "$WORK/pg/home" "$(pg_url rehearsal_pg_migrate)" \
    "pg_copy rehearsal_pg_migrate rehearsal_pg_baseline" \
    "pg_copy rehearsal_pg_migrate rehearsal_pg_after1"

  say ""
  say "== pg: new CLI against the old controller (CLI upgraded first)"
  pg_copy rehearsal_pg_baseline rehearsal_pg_oldctl
  install -d -m 700 "$WORK/pg/oldctl-home"
  if start_controller pg-oldctl "$OLD_CONTROLLER" "$WORK/pg/oldctl-home" "$(pg_url rehearsal_pg_oldctl)" "$(cat "$WORK/pg/creds/secrets-key")"; then
    cli_verify "pg: new CLI -> old controller" "$WORK/bin/new/sparkwing" "$CONTROLLER_URL" "$WORK/pg/creds" "$(jq -r .last_run.id "$WORK/pg/before.json")"
    stop_controller
  else
    fail "pg: the old controller did not start on the baseline: $(tail -2 "$WORK/pg-oldctl.log")"
  fi
fi

say ""
if [[ "$FAILURES" -gt 0 ]]; then
  say "rehearsal: $FAILURES failure(s); full log $LOG"
  exit 1
fi
say "rehearsal: all checks passed; full log $LOG"
