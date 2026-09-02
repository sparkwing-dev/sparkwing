# Delivery

Use the smallest evidence that covers the change. A lead agent owns the decision;
this file is a menu and checklist, not a command that every change must run.

## Checks

- **Cheap:** format touched Go files and run the affected package tests, for
  example `go test ./internal/orchestrator -run RunAndAwait`. The `lint`,
  `test`, and `build` pipelines are focused checks when their whole boundary is
  relevant; invoke one with `sparkwing run <name>`.
- **Normal broad check:** `sparkwing run pre-commit` covers every committed Go
  module, the dashboard TypeScript unit, full ESLint, production build,
  and browser smoke suites, formatting, vet, build, tests, documentation
  mirrors, and source policy. Unit and ESLint run in parallel; the production
  build then feeds the browser suite.
- **Expensive or release-boundary:** `sparkwing run pre-push` adds race, chaos,
  vulnerability, dependency-freshness, API, and Terraform gates. Use
  `integration`, `template-verify`, `static-analysis`, and image builds only when
  the change touches those boundaries. `sparkwing run security-scan` runs gosec,
  source-mode govulncheck, gitleaks, and `npm audit`. The Security workflow runs
  it on every pull request and uploads gosec findings to code scanning. The
  release workflow runs it and hosted CodeQL against the resolved tag commit
  before any artifact build. CodeQL reports alerts; gosec, govulncheck,
  gitleaks, and `npm audit` fail the gate. Run the local pipeline when a change
  touches an HTTP handler, auth, file paths built from input, subprocess
  arguments, or a dependency. Verify dashboard changes against real local state
  with `bash bin/dev-start.sh` (dashboard backend on :4343, `next dev` on :3100)
  and stop it with `bash bin/dev-stop.sh`; the browser gate uses deterministic
  API fixtures on OS-assigned local ports and does not replace that product
  exercise or exercise Kubernetes.
- **Template verification proofs:** `template-verify` scaffolds, builds, lints,
  and explains all 39 registry templates, and runs the runnable ones. A local
  run reuses a recorded proof for any template whose proof inputs are
  byte-identical to a previous pass. The digest covers, in one hash per
  template: the template's registry files (every file under its directory in
  the embedded `templates.FS`); the manifest's verification fields (tier,
  fixture, `verify_params`, `verify_tools`); the exact working state of the
  sparkwing checkout, which is what supplies the SDK every scaffold replaces,
  the CLI each step invokes, and this verifier's own source; the same for the
  local sparks-core checkout; `go env GOVERSION GOOS GOARCH`; and the resolved
  path and `--version` output of every host tool the template's fixture and
  `verify_tools` need, so a proof recorded while Docker was absent is not
  reused once it is present. A proof-format constant sits in the same hash, so
  widening or narrowing that list invalidates every recorded proof.

  Reuse is fail-closed. Any input that cannot be established refuses reuse and
  verifies the template again: a checkout that is not a git repository, an
  unreadable untracked file, a template that will not digest, and in
  particular the absence of a local sparks-core checkout, because without one a
  scaffold's `go mod tidy` resolves published module versions that no digest
  covers. Reuse never changes the plan: every template keeps its node, and the
  decision is made inside it, so a digest miss cannot be lost in plan shaping.
  Third-party module resolution is outside the digest either way, which is the
  standing reason the boundary stays exhaustive: the release pipeline passes
  `--exhaustive`, and so should any manual run being used as a release proof.
  Recorded proofs live under the user cache directory, one file per digest.

- **Kubernetes product path:** `sparkwing run k8s-e2e` proves authenticated
  webhook intake, runner execution, logs, cancellation, retry, restarts, and
  retained state against an explicit Kubernetes context and caller-supplied
  image set. It requires an exact namespace/release cleanup allow-list and
  never creates or deletes cluster infrastructure. This check is manual and
  opt-in because it uses a real cluster.

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
