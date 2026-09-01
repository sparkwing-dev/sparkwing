#!/usr/bin/env bash
set -e
cd "$(dirname "$0")"

WORKDIR=$(mktemp -d)
cp Dockerfile Gemfile Gemfile.lock package.json "$WORKDIR/"
trap 'rm -rf "$WORKDIR"' EXIT
cd "$WORKDIR"

RESULTS=()

time_build() {
    local label="$1"
    shift
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "  $label"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    local start end elapsed
    start=$(date +%s)
    local slug logfile
    slug="$(echo "$label" | tr ' ' '-')"
    logfile="/tmp/bench-$slug.log"
    docker build "$@" . 2>&1 | tee "$logfile" | tail -15
    end=$(date +%s)
    elapsed=$((end - start))

    local mins=$((elapsed / 60))
    local secs=$((elapsed % 60))
    echo ""
    echo "  TIME: ${mins}m ${secs}s ($elapsed seconds)"
    echo "  LOG:  $logfile"
    RESULTS+=("$label|${elapsed}")
}

echo "╔══════════════════════════════════════════════════════════════════╗"
echo "║         Sparkwing Build Cache -- Thorough Benchmark              ║"
echo "║         WARNING: docker system prune will remove all images     ║"
echo "╚══════════════════════════════════════════════════════════════════╝"

echo ""
echo "==> Nuking everything (docker system prune -af)..."
docker system prune -af > /dev/null 2>&1 || true
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "A. True cold (nothing)" \
    --no-cache -t bench-a

echo ""
echo "==> Pruning build cache only (keeping base images)..."
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "B. Images cached only" \
    --no-cache -t bench-b

time_build \
    "C. Images + warm cache mounts" \
    --no-cache -t bench-c

time_build \
    "D. Full layer cache (unchanged)" \
    -t bench-d

echo ""
echo "==> Adding dayjs to package.json, httparty to Gemfile..."
sed -i.bak 's/"uuid": "^11.0.0"/"uuid": "^11.0.0",\n    "dayjs": "^1.11.0"/' package.json
sed -i.bak 's/gem "bootsnap"/gem "bootsnap"\ngem "httparty", "~> 0.22"/' Gemfile

echo "==> Updating Gemfile.lock..."
docker run --rm -v "$PWD":/app -w /app ruby:3.3-alpine \
    sh -c "apk add --no-cache build-base postgresql-dev linux-headers gcompat > /dev/null 2>&1 && bundle lock --update 2>&1" | tail -3

time_build \
    "E. Dep change + warm mounts" \
    --no-cache -t bench-e

echo ""
echo "==> Pruning build cache (killing mounts)..."
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "F. Dep change + cold mounts" \
    --no-cache -t bench-f

echo ""
echo "==> Nuking everything (docker system prune -af)..."
docker system prune -af > /dev/null 2>&1 || true
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "G. True cold + new deps" \
    --no-cache -t bench-g

echo ""
echo ""
echo "╔═══════════════════════════════════════════════════════════════════════╗"
echo "║                       BENCHMARK RESULTS                              ║"
echo "╠═════════════════════════════════════════════╦═════════╦══════════════╣"
echo "║ Scenario                                    ║  Time   ║ What's cached║"
echo "╠═════════════════════════════════════════════╬═════════╬══════════════╣"

cache_labels=("nothing" "base images" "images+mounts" "everything" "images+mounts" "base images" "nothing")
idx=0
for r in "${RESULTS[@]}"; do
    label="${r%%|*}"
    secs="${r##*|}"
    mins=$((secs / 60))
    rem=$((secs % 60))
    cl="${cache_labels[$idx]}"
    printf "║ %-43s ║ %3dm %02ds ║ %-12s ║\n" "$label" "$mins" "$rem" "$cl"
    idx=$((idx + 1))
done

echo "╚═════════════════════════════════════════════╩═════════╩══════════════╝"

echo ""
echo "DERIVED COSTS:"
echo "─────────────────────────────────────────────────"

a="${RESULTS[0]##*|}"
b="${RESULTS[1]##*|}"
c="${RESULTS[2]##*|}"
d="${RESULTS[3]##*|}"
e="${RESULTS[4]##*|}"
f="${RESULTS[5]##*|}"
g="${RESULTS[6]##*|}"

printf "  Base image pulls:           %3ds  (A %ds - B %ds)\n" "$((a - b))" "$a" "$b"
printf "  Package install (cold):     %3ds  (B %ds - overhead)\n" "$b" "$b"
printf "  Cache mount savings:        %3ds  (F %ds - E %ds)\n" "$((f - e))" "$f" "$e"
printf "  Layer cache savings:        %3ds  (C %ds - D %ds)\n" "$((c - d))" "$c" "$d"
printf "  Docker overhead (cached):   %3ds  (D %ds)\n" "$d" "$d"
echo ""
echo "  Total with all caches:      ${d}s"
echo "  Total with no caches:       ${a}s"
