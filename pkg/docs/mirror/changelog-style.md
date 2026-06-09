# CHANGELOG style

The rubric the pre-release manicuring agent applies to `CHANGELOG.md`
and the migration guides under `docs/migrations/`. Manual edits should
follow the same conventions so the agent's diff at release time is
small.

Pairs with [VERSIONING.md](../VERSIONING.md) (the stability policy)
and the release pipeline in `.sparkwing/jobs/release.go` (which
auto-renames `## [Unreleased]` to `## [vX.Y.Z] - YYYY-MM-DD` on tag).

## CHANGELOG.md structure

One `## [Unreleased]` heading at the top, then released versions
descending below. Within each version, **exactly one block per
category** -- never duplicate sub-headings.

Categories (in this order when present):

- `### Added` -- new functionality
- `### Changed` -- existing functionality that behaves differently
- `### Fixed` -- bug fixes
- `### Removed` -- functionality deleted outright
- `### Security` -- vulnerability fixes / hardening
- `### Docs` -- user-facing documentation changes (README, embedded
  docs, examples). Internal-only design docs don't get an entry.

`### Deprecated` is omitted intentionally: sparkwing is pre-1.0 and
follows hard-cut semantics (see VERSIONING.md). Removals go straight
into `### Removed` with a (Breaking) marker.

## Entry shape

Every bullet starts with a bold scope prefix followed by a short
title-line, then optional prose:

```markdown
- **<scope>:** Title-line sentence (no trailing period on the title).
  Optional follow-up prose, migration table, etc.
```

Scope is freeform -- pick the smallest accurate label. Common
scopes:

- `sdk` -- the `sparkwing/` package (author-facing API)
- `cli` -- `cmd/sparkwing/` user surface
- `orchestrator` -- internal dispatch engine
- `controller` / `runner` / `cache` / `logs` / `web` -- per-binary
- `release` -- the release pipeline + CHANGELOG/migration tooling
- `docs` -- documentation-only changes
- `deps` -- dependency bumps

Multiple scopes are fine when an entry truly spans two: `**sdk +
cli:**`. If an entry needs more than two scopes, it probably needs
to be split.

## Breaking changes

Mark inline with `(Breaking)` directly after the scope:

```markdown
### Changed

- **sdk (Breaking):** `Needs(...any)` replaced with `Needs(...Dep)`.
  See [migration guide](migrations/v0.4.0.md#typed-dep-interface) for
  the multi-step pattern. Summary: typed Plan-layer dependency wiring;
  by-name string references no longer accepted.
```

Every `(Breaking)` entry MUST link to a section in the release's
migration guide. The agent generates the guide at release time from
the breaking entries in `[Unreleased]`.

## Migration guides

One file per release: `docs/migrations/v<X.Y.Z>.md`. Target version in
the filename; the "from" version is always the prior release. Adopters
jumping multiple versions read the files in chronological order.

Always created -- even for releases with one small breaking change --
so `https://sparkwing.dev/docs/migration-guide/v<X.Y.Z>` resolves
predictably and the surface is consistent.

### File shape

```markdown
# Migrating to v0.4.0

One-paragraph summary of what changed at a glance. Whether the
migration is mostly mechanical, whether ordering matters, total
time estimate if non-trivial.

## Typed Dep interface

Anchor matches the link from the CHANGELOG entry. One H2 per breaking
change. Inside each H2:

- **Before:** code snippet showing the old shape
- **After:** code snippet showing the new shape
- **Why:** the motivation (one paragraph, optional)
- **Edge cases / gotchas:** anything the inline CHANGELOG table can't
  fit (multi-step ordering, deprecated patterns, sibling repos affected)

## CacheOptions rename

(next breaking change)
```

H2 anchors are how the CHANGELOG entry links into a specific
migration. Stable anchor slugs matter; rename with care.

### Index

`docs/migrations/README.md` is an append-only chronological index.
The release pipeline appends an entry on every release.

## What the linter enforces vs what the agent does

Two layers, with deliberately separate concerns:

- **`sparkwing run lint`** (the CHANGELOG-style gate in
  `.sparkwing/jobs/changelog_lint.go`) catches the mechanical
  violations: duplicate `### Category` sub-headings inside a single
  version section, missing or wrong migration-guide links on
  `(Breaking)` entries (file must exist, anchor must resolve to an
  H2 in the file, version in the link path must match the section's
  version). Output shape: `CHANGELOG.md:<line>: <category>: <message>`,
  one issue per violation, exit non-zero with a final `<N> issue(s)`
  summary. Always-on; no env-var gate.
- **The pre-release manicuring agent** does the judgment work: tightening
  prose, choosing scope prefixes, deciding which entries to merge,
  generating the migration-guide bodies, pulling internal-cleanup
  entries that don't belong in adopter-facing notes. The linter
  surfaces *what's wrong*; the agent decides *how to fix it*.

## Pre-release manicuring (what the agent does)

Before cutting `vX.Y.Z`, the agent applies this rubric to `[Unreleased]`:

1. **Dedupe sub-headings.** Collapse multiple `### Added`/`### Changed`/
   etc. into one block per category. Order: Added, Changed, Fixed,
   Removed, Security, Docs.
2. **Add scope prefixes.** Every bullet gets `**<scope>:**`. Existing
   prose stays; only the prefix is added.
3. **Surface breaking changes.** Every breaking entry gets the
   `(Breaking)` marker inline after the scope.
4. **Generate the migration guide.** Create `docs/migrations/v<X.Y.Z>.md`
   with one H2 per breaking entry. Link each breaking entry to its
   anchor.
5. **Append to the migration index.** Add the new file to
   `docs/migrations/README.md`.
6. **Optional polish.** Tighten prose; merge near-duplicate entries
   (e.g., two `### Added` bullets for related work); pull pure
   internal-cleanup entries that don't belong (those are dev-facing,
   not adopter-facing).

The release pipeline (`sparkwing run release --version vX.Y.Z`) then
takes over: validates `[vX.Y.Z]` doesn't exist yet, renames
`[Unreleased]` to `[vX.Y.Z] - YYYY-MM-DD`, commits, pushes branch +
tag, GH Actions extracts the section as the GitHub Release body.

## What goes in `[Unreleased]` between releases

Same shape as a released section, just under the `[Unreleased]`
heading. Breaking entries can omit the migration-guide link until
release time -- the agent fills those in when it generates the
guide. Or include a placeholder: `(migration guide TBD)`.
