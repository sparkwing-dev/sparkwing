# Versioning Policy

Sparkwing follows semantic versioning with explicit scope: only certain parts of the codebase carry stability promises. This document defines what is covered, what is not, and how breaking changes are announced.

## What's covered

| Scope | Promise |
|---|---|
| `pkg/...` packages | Public API. Breaking changes only in major version bumps (or minor while pre-1.0). |
| Top-level `sparkwing/` package | Author SDK. Same promise as `pkg/...`. |
| CLI flags (`sparkwing` and subcommands) | Public surface. Renames or removals follow the deprecation procedure below. |
| Wire protocols (HTTP API request/response shapes, persisted JSON record shapes) | Treated as public API. JSON field renames or type changes are breaking. |
| YAML config formats (`pipelines.yaml`, `runners.yaml`, `sources.yaml`, `backends.yaml`, `profiles.yaml`) | Public surface. Field renames or removals are breaking. |

## What's NOT covered

| Scope | Status |
|---|---|
| `internal/...` packages | Implementation detail. Can change anytime, even in patch releases. External consumers cannot import these (Go-enforced). |
| `cmd/...` binary internals | The CLI flag surface is covered (above), but the internals of how each binary is built are not. |
| Test fixtures, test helpers, `_test.go` files | Not part of the API surface. |
| Embedded documentation, examples, scaffold templates | May be revised at any time. |

## What's a breaking change

Any of the following on covered surfaces:

- Removing or renaming an exported symbol (type, function, method, constant, variable, field)
- Changing the signature of an exported function or method (adding required params, changing return types)
- Adding a method to an exported interface (forces existing implementations to update)
- Changing JSON field names or types in wire-format structs
- Renaming or removing a CLI flag
- Changing CLI flag behavior in a way callers cannot reasonably handle (e.g., changing the default value)
- Removing or renaming a YAML field that consumers write
- Renaming or removing a binary

## Deprecation procedure

When a covered API is on its way out:

1. Mark it with a `// Deprecated: <reason>. Use X instead.` godoc comment (Go convention; IDEs surface this).
2. For CLI flags and SDK functions, emit a runtime warning when the deprecated path is hit. Format: `WARN: <symbol> is deprecated; use <replacement> instead. See CHANGELOG.md.`
3. Add a `Deprecated` entry to `CHANGELOG.md` in the same release.
4. Keep the deprecated symbol working for at least one minor release.
5. Remove the symbol in a subsequent major release (or minor while pre-1.0).

The runtime warning is important -- it catches uses that the godoc comment misses (e.g., dynamic callers, generated code).

## Pre-1.0 caveat

While Sparkwing is at `v0.x.y`, minor bumps may contain breaking changes per Go semver convention. The deprecation procedure still applies -- breaking changes are announced with at least one release of warning before removal. Once Sparkwing reaches `v1.0.0`, breaking changes will be confined to major bumps.

## Release process

- Every user-visible change requires a `CHANGELOG.md` entry under the current `[Unreleased]` section.
- Sections follow [Keep a Changelog](https://keepachangelog.com/): `Added`, `Changed`, `Fixed`, `Removed`, `Security`, `Docs`. (`Deprecated` is omitted -- sparkwing is pre-1.0 and follows hard-cut semantics; removals go straight into `Removed` with a `(Breaking)` marker.)
- Entry format: bold scope prefix, `(Breaking)` inline for breaks, link to migration guide. See [docs/changelog-style.md](./docs/changelog-style.md) for the rubric the pre-release manicuring agent applies.
- Every breaking change in a release gets a corresponding section in `docs/migrations/v<X.Y.Z>.md`. Files are always created (even for single-break releases) so the migration-guide URL is consistent per release.
- CI fails if a commit touches `pkg/`, `sparkwing/`, CLI flag definitions, or wire-format structs without including a `CHANGELOG.md` entry. The gate lives in `bin/check-changelog.sh` and runs as part of `sparkwing run lint`.
- The release pipeline (`sparkwing run release --version vX.Y.Z`) renames `[Unreleased]` to `[vX.Y.Z] - YYYY-MM-DD` and commits before tagging. The GH-Actions release workflow extracts that section verbatim as the GitHub Release body via `bin/extract-changelog-section.sh`.

## Wire protocol

The controller's HTTP API has a formal contract at
[`api/openapi.yaml`](./api/openapi.yaml) (OpenAPI 3.0). Wire-protocol
changes follow the same semver discipline as Go API changes:

- Renaming a JSON field, removing a field, or changing a field's
  type is a **breaking change**. The deprecation procedure above
  applies -- announce in a `Changed` / `Deprecated` CHANGELOG entry
  one release ahead of removal.
- Adding a new optional field, adding a new route, or adding a new
  status code is **non-breaking** when existing callers ignore it.
- Changing a route's path, HTTP method, or required-vs-optional
  status on a field is breaking.

The OpenAPI spec is the source of truth for what the controller
serves; if reality and the spec diverge, the spec is wrong (fix
it). Keeping it in sync is human discipline today -- there is no
automated drift gate for the HTTP surface yet (the snapshot gate
below covers the Go surface only).

## API surface snapshot

A deterministic text snapshot of the entire covered public API
lives under `.apidiff/`, one file per package
(`pkg_storage.txt`, `sparkwing.txt`, …). The snapshot is the
machine-readable source of truth for what the API looks like at HEAD;
godoc comments are deliberately excluded so the file diff captures
only contract-affecting changes.

The lint pipeline (`sparkwing run lint`) regenerates the snapshots
into a tempdir and diffs against the checked-in tree.
**PRs that change the public surface without updating `.apidiff/`
fail CI** -- the snapshot must be regenerated and committed in the
same PR.

Workflow when you change a covered API:

1. Make the source change.
2. Run `bash bin/regen-api-snapshot.sh`.
3. Review the resulting `.apidiff/` diff -- that's the surface change
   reviewers will see.
4. Add a `CHANGELOG.md` entry under `[Unreleased]` (Added / Changed /
   Removed / Deprecated).
5. Commit both the source and the snapshot in the same PR.

Snapshot diffs are the single most useful artifact in API-affecting
review: a reviewer scanning the PR sees exactly which exported
symbols moved, in what direction, with no other noise.

## Conformance suites for plug-in interfaces

The plug-in interfaces under `pkg/storage` and `pkg/controller`
ship portable test suites so adopters writing custom implementations
can verify they honor the contract:

| Interface | Suite |
|---|---|
| [`storage.ArtifactStore`](./pkg/storage/storage.go) | [`pkg/storage/conformance.TestArtifactStore`](./pkg/storage/conformance) |
| [`storage.LogStore`](./pkg/storage/storage.go) | [`pkg/storage/conformance.TestLogStore`](./pkg/storage/conformance) |
| [`controller.Cipher`](./pkg/controller/cipher.go) | [`pkg/controller/ciphertest.TestCipher`](./pkg/controller/ciphertest) |

Each suite is a single exported `TestX(t, factory)` function. A
downstream implementation calls it from its own `*_test.go` with a
factory that returns a fresh implementation per subtest. Operations
the implementation opts out of (`storage.ErrNotSupported`,
`storage.ErrListNotSupported`) are skipped, not failed.

The conformance contract counts as part of the public API: changes
to what an implementation must support to pass are breaking, and
follow the same deprecation procedure as Go-level API changes.

## Migration help

When a breaking change ships:

- The CHANGELOG entry includes the scope, a `(Breaking)` marker, the symbol/flag/field being removed or renamed, and a link to the matching section of `docs/migrations/v<X.Y.Z>.md`.
- The migration guide carries the longer-form before/after code, multi-step ordering, gotchas, and any sibling-repo impact. Adopters scanning the release page see the short summary; adopters actively migrating click through to the detailed steps.
- Every release has a migration guide file, even for releases with one small break -- the URL shape `https://sparkwing.dev/docs/migration-guide/v<X.Y.Z>` resolves predictably and lets downstream pages link reliably.

Full format conventions live in [docs/changelog-style.md](./docs/changelog-style.md).
