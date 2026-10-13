# CHANGELOG style

The rubric the pre-release manicuring agent applies to `CHANGELOG.md`
and the migration guides under `docs/migrations/`. Manual edits should
follow the same conventions so the agent's diff at release time is
small.

Pairs with [VERSIONING.md](VERSIONING.md) (the stability policy)
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
  See [migration guide](docs/migrations/v0.4.0.md#typed-dep-interface) for
  the multi-step pattern. Summary: typed Plan-layer dependency wiring;
  by-name string references no longer accepted.
```

The marker sits **inside** the bold scope: `- **sdk (Breaking):**`, not
`- **sdk:** (Breaking)`. Only the first shape is recognized, so an entry
written the other way is invisible to every check below -- including the
one that requires a migration guide to exist.

Until paid launch nothing is carried forward for existing users, so a
`(Breaking)` entry needs no migration guide. A link it does carry must
resolve: write that section in `docs/migrations/_unreleased.md` and link
the entry to its anchor there; the release pipeline rolls the file to
`vX.Y.Z.md` and repoints the link.

## Migration guides

One file per release that contains a breaking change:
`docs/migrations/v<X.Y.Z>.md`. Target version in
the filename; the "from" version is always the prior release. Adopters
jumping multiple versions read the files in chronological order.

`docs/migrations/_unreleased.md` holds the sections the next release
owes. `sparkwing run release` renames it to `v<X.Y.Z>.md`, retitles it
`# Migrating to v<X.Y.Z>`, leaves a fresh `_unreleased.md` behind it, and
commits that with the changelog rename, so each `(Breaking)` entry's link
resolves and `https://sparkwing.dev/docs/migration-guide/v<X.Y.Z>` is
stable for the releases that need it. A release with no breaking changes
has no guide file.

Retitling rewrites the `#` line and nothing else, so the paragraph under it
is yours: write the release's opening summary into `_unreleased.md` as the
sections land, not at cut time.

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

`docs/migrations/README.md` is an append-only chronological index. The
release pipeline adds the row when it rolls the guide, dated with the
changelog section's own date and summarized from the first sentence of
each `(Breaking)` entry. Tighten that summary by hand afterwards if it
reads long.

## What the linter enforces vs what the agent does

Two layers, with deliberately separate concerns:

- **`sparkwing run lint`** (the CHANGELOG-style gate in
  `.sparkwing/jobs/changelog_lint.go`) catches the mechanical
  violations: duplicate `### Category` sub-headings inside a single
  version section, broken migration-guide links on `(Breaking)` entries
  (file must exist, anchor must resolve to an H2 in the file, version in
  the link path must match the section's version), and, at release time,
  an undescribed runs-store schema change: when the embedded schema
  version differs from the previous release, the section being cut -- or
  `[Unreleased]` before the rename -- needs a `(Breaking)` entry naming
  the schema (`unmarked-schema-break`). Output shape:
  `CHANGELOG.md:<line>: <category>: <message>`, one issue per violation,
  exit non-zero with a final `<N> issue(s)` summary. Always-on; no
  env-var gate.
- **The pre-release manicuring agent** does the judgment work: tightening
  prose, choosing scope prefixes, deciding which entries to merge,
  writing the migration-guide bodies into
  `docs/migrations/_unreleased.md`, pulling internal-cleanup entries
  that don't belong in adopter-facing notes. The linter surfaces
  *what's wrong*; the agent decides *how to fix it*; the release
  pipeline does the mechanical rename, index row and link repointing.

## Runs-store schema changes

The store has one baseline schema, `pkg/store/schema_sqlite.sql` and
`pkg/store/schema_postgres.sql`, and no migrations: a build refuses a
store at any other schema version. Change both files together and bump
`expectedSchemaVersion` in `pkg/store/store.go`, which strands every
existing store, so the change needs a `(Breaking)` entry naming the
schema. The release refuses a tag whose baseline SQL differs from the
previous release's while the version stays the same. Compatibility, and migrations with it, start again at paid
launch with the baseline as version 1.

## Pre-release manicuring (what the agent does)

Before cutting `vX.Y.Z`, the agent applies this rubric to `[Unreleased]`:

1. **Dedupe sub-headings.** Collapse multiple `### Added`/`### Changed`/
   etc. into one block per category. Order: Added, Changed, Fixed,
   Removed, Security, Docs.
2. **Add scope prefixes.** Every bullet gets `**<scope>:**`. Existing
   prose stays; only the prefix is added.
3. **Surface breaking changes.** Every breaking entry gets the
   `(Breaking)` marker inline after the scope.
4. **Check the migration guide.** Each link a breaking entry carries
   needs its H2 in `docs/migrations/_unreleased.md`. The release pipeline
   rolls the file and the index; it refuses to tag when a link names no
   section, because the prose is yours to write.
5. **Optional polish.** Tighten prose; merge near-duplicate entries
   (e.g., two `### Added` bullets for related work); pull pure
   internal-cleanup entries that don't belong (those are dev-facing,
   not adopter-facing).

The release pipeline (`sparkwing run release --version vX.Y.Z`) then
takes over: validates `[vX.Y.Z]` doesn't exist yet, renames
`[Unreleased]` to `[vX.Y.Z] - YYYY-MM-DD`, rolls
`docs/migrations/_unreleased.md` to `vX.Y.Z.md` with its index row and
repoints the section's `(Breaking)` links at it, commits all of that as
one change, pushes branch + tag, GH Actions extracts the section as the
GitHub Release body. `--sw-dry-run` prints what it would rename, repoint
and index without writing anything. The workflow's `release-verify`
stage judges the same on the tagged source, so a tag pushed by hand is
held to it too.

## What goes in `[Unreleased]` between releases

Same shape as a released section, just under the `[Unreleased]`
heading. A breaking entry that has a guide links
`docs/migrations/_unreleased.md#<anchor>`, which the linter accepts
inside `[Unreleased]` and which the release repoints to the rolled
guide. Until paid launch a breaking entry may carry no link at all.
