# Delivery

Use the smallest evidence that covers the change. A lead agent owns the decision;
this file is a menu and checklist, not a command that every change must run.

## Checks

- **Cheap:** format touched Go files and run the affected package tests, for
  example `go test ./internal/orchestrator -run RunAndAwait`. The `lint`,
  `test`, and `build` pipelines are focused checks when their whole boundary is
  relevant; invoke one with `sparkwing run <name>`.
- **Normal broad check:** `sparkwing run pre-commit` covers every committed Go
  module, the dashboard TypeScript unit, production build, browser-test lint,
  and browser smoke suites, formatting, vet, build, tests, documentation
  mirrors, and source policy. Unit and browser-test lint run in parallel; the
  production build then feeds the browser suite. A measured warm browser chain
  takes about 18 seconds (0.7s unit, 2.2s lint, 4.5s build, 11.0s browser,
  including its cached browser install and authenticated Go fixture).
- **Expensive or release-boundary:** `sparkwing run pre-push` adds race, chaos,
  vulnerability, dependency-freshness, API, and Terraform gates. Use
  `integration`, `template-verify`, `static-analysis`, and image builds only when
  the change touches those boundaries. Verify dashboard changes against real
  local state with `bash bin/dev-start.sh` (dashboard backend on :4343, `next
  dev` on :3100) and stop it with `bash bin/dev-stop.sh`; the browser gate uses
  deterministic API fixtures on OS-assigned local ports and does not replace
  that product exercise or exercise Kubernetes.
- **Kubernetes product path:** `sparkwing run kind-e2e` builds all five images
  from the checkout, loads them into a disposable local Kind cluster, installs
  the full chart, and proves authenticated webhook intake, runner execution,
  logs, cancellation, retry, restarts, and retained state. It requires Docker,
  Kind, kubectl, Helm, curl, jq, git, and OpenSSL, but no registry or cloud
  cluster. The path-scoped hosted workflow runs the same pipeline for
  controller, runner, chart, and persistence changes and uploads failure
  evidence. Set `SPARKWING_KIND_E2E_KEEP_CLUSTER=1` to retain a failed local
  cluster for inspection.

## Decisions before landing

- **Review:** use an independent reviewer for public SDK/CLI contracts, cache or
  admission invariants, concurrency, process lifecycle, persistent schemas,
  security boundaries, or release machinery. Otherwise state why it was not
  valuable.
- **Documentation:** keep README, CLI help, source docs, examples, generated
  references, and `pkg/docs/mirror/` aligned with behavior. Run the owning drift
  check instead of editing generated mirrors by hand.
- **Changelog:** notable adopter-facing behavior belongs in `[Unreleased]` and
  follows `docs/changelog-style.md`. Mark breaking changes and supply migration
  guidance before release. Keep the embedded changelog mirror byte-identical.
- **Tests:** record the focused checks selected, or why execution was waived.
  Do not run every race, Docker, or integration suite by default.
- **Release:** merging is not a release. A release is an explicit operator
  decision: preview with `SPARKWING_HOME="$(mktemp -d)" sparkwing run release
  --sw-dry-run`, then use `SPARKWING_HOME="$(mktemp -d)" sparkwing run release
  --version vX.Y.Z --sw-allow destructive,prod` to rewrite the changelog and
  push the tag; the isolated home keeps prerelease state out of the operational
  runs store, which the release runner refuses to touch. GitHub Actions owns
  public binaries and images after that tag.
- **Independent verification:** for user-facing local-execution changes, build
  the intended revision with `SKIP_WEB_BUILD=1 bash bin/install.sh` when the web
  bundle is unchanged, then exercise the installed CLI and daemon. Verify SDK,
  templates, integrations, browser behavior, or release assets when those
  surfaces changed.
