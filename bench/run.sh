#!/usr/bin/env bash
#
# Sparkwing Proxy Benchmark
#
# Demonstrates the impact of sparkwing-cache on Docker build times
# for a full-stack Ruby + Node application.
#
# Scenarios:
#   1. Cold build (no proxy, no cache)     — baseline, everything from internet
#   2. Rebuild (Docker layer cache)         — fast, layers already exist
#   3. Cold build with proxy (proxy warms)  — similar to #1, but populates proxy
#   4. New base image + proxy (proxy warm)  — THE MONEY SHOT: layers busted, proxy saves the day
#   5. New base image, no proxy             — comparison: how slow without proxy
#
set -e
cd "$(dirname "$0")"

PROXY_PORT=9777
PROXY_PID=""
RESULTS=()

# ── helpers ──────────────────────────────────────────────

cleanup() {
    if [ -n "$PROXY_PID" ]; then
        kill "$PROXY_PID" 2>/dev/null || true
        wait "$PROXY_PID" 2>/dev/null || true
    fi
    rm -f /tmp/sparkwing-cache
}
trap cleanup EXIT

time_build() {
    local label="$1"
    shift
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "  $label"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    local start end elapsed
    start=$(date +%s)
    docker build "$@" . 2>&1 | tail -20
    end=$(date +%s)
    elapsed=$((end - start))

    local mins=$((elapsed / 60))
    local secs=$((elapsed % 60))
    echo ""
    echo "  ⏱  ${mins}m ${secs}s ($elapsed seconds)"
    RESULTS+=("$label|${elapsed}")
}

# ── start proxy ──────────────────────────────────────────

echo "Building sparkwing-cache..."
(cd .. && go build -o /tmp/sparkwing-cache ./cmd/sparkwing-cache/)

PROXY_DIR=$(mktemp -d)
PROXY_CACHE_DIR="$PROXY_DIR" PORT=$PROXY_PORT /tmp/sparkwing-cache &
PROXY_PID=$!

for i in $(seq 1 30); do
    if curl -sf "http://localhost:$PROXY_PORT/health" > /dev/null 2>&1; then
        echo "Proxy ready on :$PROXY_PORT (cache: $PROXY_DIR)"
        break
    fi
    sleep 0.3
done

PROXY_URL="http://host.docker.internal:$PROXY_PORT"

# ── Gemfile.lock ─────────────────────────────────────────
# Generate Gemfile.lock if missing (needed for bundle install in Docker)
if [ ! -f Gemfile.lock ]; then
    echo "Generating Gemfile.lock (one-time)..."
    docker run --rm -v "$PWD":/app -w /app ruby:3.3-alpine \
        sh -c "apk add --no-cache build-base postgresql-dev linux-headers gcompat && bundle lock" 2>&1 | tail -3
fi

# ── benchmarks ───────────────────────────────────────────

# 1. Cold build — no proxy, no cache
echo ""
echo "Clearing Docker build cache..."
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "1. Cold build (no proxy)" \
    --no-cache \
    --build-arg PROXY_URL="" \
    -t bench-cold

# 2. Rebuild — Docker layer cache
time_build \
    "2. Warm rebuild (Docker cache)" \
    --build-arg PROXY_URL="" \
    -t bench-warm

# 3. Cold build with proxy — warms the proxy cache
echo ""
echo "Clearing Docker build cache..."
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "3. Cold build + proxy (proxy warms)" \
    --no-cache \
    --build-arg PROXY_URL="$PROXY_URL" \
    -t bench-proxy-warm

# Show proxy stats
echo ""
echo "Proxy cache after warming:"
curl -s "http://localhost:$PROXY_PORT/stats" | python3 -m json.tool 2>/dev/null || \
    curl -s "http://localhost:$PROXY_PORT/stats"

# 4. New base image + proxy — cache busted, proxy saves the day
echo ""
echo "Clearing Docker build cache..."
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "4. New base image + proxy (cached)" \
    --no-cache \
    --build-arg NODE_IMAGE=node:20-alpine \
    --build-arg RUBY_IMAGE=ruby:3.2-alpine \
    --build-arg PROXY_URL="$PROXY_URL" \
    -t bench-newbase-proxy

# 5. New base image, no proxy — comparison
echo ""
echo "Clearing Docker build cache..."
docker builder prune -af > /dev/null 2>&1 || true

time_build \
    "5. New base image, no proxy" \
    --no-cache \
    --build-arg NODE_IMAGE=node:20-alpine \
    --build-arg RUBY_IMAGE=ruby:3.2-alpine \
    --build-arg PROXY_URL="" \
    -t bench-newbase-noproxy

# ── results ──────────────────────────────────────────────

echo ""
echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║                    BENCHMARK RESULTS                        ║"
echo "╠══════════════════════════════════════════════════════════════╣"

for r in "${RESULTS[@]}"; do
    label="${r%%|*}"
    secs="${r##*|}"
    mins=$((secs / 60))
    rem=$((secs % 60))
    printf "║  %-44s  %3dm %02ds  ║\n" "$label" "$mins" "$rem"
done

echo "╚══════════════════════════════════════════════════════════════╝"

# Compute speedup
if [ ${#RESULTS[@]} -ge 5 ]; then
    cold="${RESULTS[0]##*|}"
    newbase_proxy="${RESULTS[3]##*|}"
    newbase_noproxy="${RESULTS[4]##*|}"

    if [ "$newbase_proxy" -gt 0 ]; then
        speedup_vs_cold=$(echo "scale=1; $cold / $newbase_proxy" | bc 2>/dev/null || echo "?")
        speedup_vs_noproxy=$(echo "scale=1; $newbase_noproxy / $newbase_proxy" | bc 2>/dev/null || echo "?")
        echo ""
        echo "Proxy speedup on base image change: ${speedup_vs_noproxy}x faster"
        echo "vs cold build: ${speedup_vs_cold}x faster"
    fi
fi

echo ""
echo "Proxy cache size:"
du -sh "$PROXY_DIR"/* 2>/dev/null | while IFS= read -r line; do
    echo "  $line"
done
