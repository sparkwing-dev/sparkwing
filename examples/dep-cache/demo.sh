#!/usr/bin/env bash
# Demonstrates .CacheDir(sparkwing.GoModules()): the same pipeline run
# twice, where the first run downloads the app's Go modules and saves
# the module cache, and the second run starts from a wiped module
# cache (a fresh runner pod, simulated) and restores it instead of
# re-downloading. The second run executes with GOPROXY=off, so if the
# restore did not work, the run fails -- the proof is structural, not
# a log line.
#
# Run from anywhere inside a sparkwing checkout:
#
#   bash examples/dep-cache/demo.sh
#
# Everything lands in a throwaway temp dir (isolated SPARKWING_HOME,
# isolated GOMODCACHE); your real caches are never touched.
set -euo pipefail

SPARKWING_REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DEMO="$(mktemp -d)"
# Module caches are read-only on disk; make them writable before the
# sweep or the trap spews permission errors.
trap 'chmod -R u+w "$DEMO" 2>/dev/null || true; rm -rf "$DEMO"' EXIT

APP="$DEMO/app"
HOME_DIR="$DEMO/sparkwing-home"
MODCACHE="$DEMO/gomodcache"
mkdir -p "$APP/.sparkwing" "$HOME_DIR"

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

say "scaffolding demo app in $APP"

cat >"$APP/go.mod" <<'EOF'
module example.com/depcache-demo

go 1.26
EOF

cat >"$APP/ids.go" <<'EOF'
// Package demo exists to pull real third-party modules so the demo
// has something worth caching.
package demo

import (
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

// NewID returns a fresh UUID string.
func NewID() string { return uuid.NewString() }

// RoundTrip YAML-encodes and decodes v to prove the dep resolves.
func RoundTrip(v map[string]string) (map[string]string, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}
EOF

cat >"$APP/ids_test.go" <<'EOF'
package demo

import "testing"

func TestNewID(t *testing.T) {
	if a, b := NewID(), NewID(); a == b {
		t.Fatalf("uuids collided: %s", a)
	}
}

func TestRoundTrip(t *testing.T) {
	in := map[string]string{"k": "v"}
	out, err := RoundTrip(in)
	if err != nil {
		t.Fatal(err)
	}
	if out["k"] != "v" {
		t.Fatalf("round trip lost data: %v", out)
	}
}
EOF

cat >"$APP/.sparkwing/go.mod" <<EOF
module depcache-demo-pipelines

go 1.26

require github.com/sparkwing-dev/sparkwing v0.0.0

replace github.com/sparkwing-dev/sparkwing => $SPARKWING_REPO
EOF

cat >"$APP/.sparkwing/main.go" <<'EOF'
// Command depcache-demo-pipelines registers the demo's one pipeline:
// a test node that declares the Go module cache as a dependency
// cache.
package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/pkg/runner"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func main() { runner.Main() }

// Test runs the demo app's go test with a persistent module cache.
type Test struct{ sparkwing.Base }

func (Test) ShortHelp() string { return "go test with a dependency-cached GOMODCACHE" }

func (p *Test) Plan(_ context.Context, plan *sparkwing.Plan, _ sparkwing.NoInputs, rc sparkwing.RunContext) error {
	sparkwing.Job(plan, rc.Pipeline, p.run).
		CacheDir(sparkwing.GoModules())
	return nil
}

func (p *Test) run(ctx context.Context) error {
	_, err := sparkwing.Bash(ctx, "go test ./...").Run()
	return err
}

func init() {
	sparkwing.Register("test", func() sparkwing.Pipeline[sparkwing.NoInputs] { return &Test{} })
}
EOF

cat >"$APP/.sparkwing/sparkwing.yaml" <<'EOF'
pipelines:
  - name: test
    entrypoint: Test
EOF

say "building the pipeline binary and a matching CLI (uses your normal Go caches)"
(cd "$APP/.sparkwing" && GOWORK=off go mod tidy -e >/dev/null 2>&1 && GOWORK=off go build -o "$DEMO/pipeline" .)
# The pipeline binary hands local admission to the `sparkwing` on
# PATH; an older installed CLI cannot host this branch's daemon, so
# the demo builds its own and puts it first.
mkdir -p "$DEMO/bin"
(cd "$SPARKWING_REPO" && GOWORK=off go build -o "$DEMO/bin/sparkwing" ./cmd/sparkwing)
export PATH="$DEMO/bin:$PATH"

say "generating the app's go.sum (throwaway module cache)"
(cd "$APP" && GOWORK=off GOMODCACHE="$DEMO/tidy-cache" go mod tidy >/dev/null 2>&1)
rm -rf "$MODCACHE"

say "RUN 1 -- cold: modules download, cache saves"
(cd "$APP" && time env GOWORK=off SPARKWING_HOME="$HOME_DIR" GOMODCACHE="$MODCACHE" \
    "$DEMO/pipeline" test)

say "simulating a fresh runner pod: wiping GOMODCACHE"
chmod -R u+w "$MODCACHE" 2>/dev/null || true
rm -rf "$MODCACHE"

say "RUN 2 -- warm: cache restores; GOPROXY=off proves no re-download"
(cd "$APP" && time env GOWORK=off SPARKWING_HOME="$HOME_DIR" GOMODCACHE="$MODCACHE" \
    GOPROXY=off GOFLAGS=-mod=mod \
    "$DEMO/pipeline" test)

say "cache contents under \$SPARKWING_HOME/depcache"
ls -lh "$HOME_DIR/depcache"

say "demo complete: run 2 passed with the network path to the Go module proxy disabled"
