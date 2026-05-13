# Versioning and the plugin ecosystem

How sparkwing, sparks-core, and third-party plugins version
themselves, what compatibility you can expect, and the architectural
choices behind it. If you're authoring a plugin today, jump to
[**What this means for plugin authors**](#what-this-means-for-plugin-authors).

## Where we are today

Sparkwing ships as a single Go module:
`github.com/sparkwing-dev/sparkwing`. Plugin authors import
`github.com/sparkwing-dev/sparkwing/sparkwing` and get the entire
contract surface — Plan, Job, Work, Register, Ref, the
DAG-construction verbs, plus convenience helpers (Bash, Exec,
Logger, etc.).

We are intentionally on the `v0.x.y` line. v0 has no semver
stability promise — minors can break things, patches can introduce
new APIs. We are using v0's flexibility to iterate the contract.

A v1.x.y line was briefly published prior to public launch and has
been **retracted**. The `retract` block in `go.mod` is the
authoritative list. v1.x snapshots remain in `proxy.golang.org` (Go
proxy snapshots are immutable) but are not supported. Do not pin to
them.

## Versioning per repo

Three repos participate in plugin compatibility:

- `sparkwing` — SDK + runtime + CLI as one Go module.
- `sparks-core` — first-party plugins, one Go module per top-level
  subdirectory (aws, checks, deploy, docker, gitops, kube,
  pipelines, s3, step, templates).
- Third-party `sparks-*` plugins — independent Go modules with
  their own cadence.

Each follows standard semver within its own line: major = breaking
change, minor = additive, patch = fixes only. The interesting
question is what "compatible" means *across* these repos.

## How they relate

Any plugin (sparks-core or third-party) is a Go library that
imports sparkwing. Its published `require` line carries an implicit
"works with sparkwing v0.X.Y" claim. Consumers pulling in both will
have Go's MVS pick the highest sparkwing version required across
the dep graph.

This produces two failure modes that resolve cleanly but break at
build time:

1. **Old transitive dep wins.** Consumer pins sparks-core/aws v0.21
   (which requires sparkwing v1.1) and also pins sparkwing v0.2.1.
   MVS picks v1.1 (numerically higher, retracted). Consumer's
   freshly-migrated code fails against v1.1's API.
2. **New transitive dep wins.** Consumer pins sparks-core/pipelines
   v0.22 (built against sparkwing v0.2) alongside another plugin
   v1.5 (built against sparkwing v0.5). MVS picks v0.5;
   sparks-core/pipelines fails to compile against it.

The Go toolchain doesn't catch either at resolve time. Maintainers
keep things compatible via discipline; consumers feel the pain when
discipline slips.

For v0 we accept this and rely on:
- Migration recipes shipped in CHANGELOG.md alongside breaking
  releases.
- Mechanical-rewrite scripts for the migrations.
- Coordinated release of sparkwing + sparks-core when sparkwing
  breaks plugin-facing APIs.

## Compatibility coordinate (post-v1)

When sparkwing reaches v1, we plan to lean on Go's path-encoded
major version as the cross-repo compatibility signal:

| Era | sparkwing path | sparks-core path | Compat rule |
|---|---|---|---|
| v0 | `.../sparkwing` | `.../sparks-core/<sub>` | none — pin specific versions |
| v1 | `.../sparkwing` (v0 and v1 share path in Go) | `.../sparks-core/<sub>` | anything `v1.x.y` on any repo works with anything else `v1.x.y` |
| v2 | `.../sparkwing/v2` | `.../sparks-core/<sub>/v2` | distinct module path; cannot collide with v1 |

The promise at v1.0: **anything tagged v1 on any sparks-* module
works with sparkwing v1**, regardless of specific minor / patch.
Within v1 each module's minor and patch iterate independently.
sparkwing v1.3 + sparks-core/aws v1.5 + sparks-core/pipelines v1.12
all coexist cleanly.

When we eventually cut sparkwing v2, we cut sparks-core v2 in the
same window. v1 stays alive on its own path indefinitely; Go's
path-major rule makes them distinct modules, so consumers who don't
migrate keep working.

## Why we are not extracting an SDK module yet

A common architectural move at this point would be to split the
plugin contract out of the runtime into a separate module — call it
`github.com/sparkwing-dev/sparkwing-sdk`. The runtime would depend
on it; plugins would depend on it. We're deliberately *not* doing
this yet.

**Extraction adds real maintainer cost.** Two modules to release in
coordination, interfaces where today there are direct method calls,
type aliases for backward compatibility, multi-repo refactors when
the contract shifts. The "clean architecture" framing tends to
gloss over this; it's a real ongoing tax.

**The benefit is mostly to plugin authors.** A small stable SDK
module shields plugins from runtime churn. That's real, but it
scales with the size of the plugin ecosystem.

**Sparkwing has fewer than ~20 plugins today.** Migration tooling
is enough at this scale: we rebaselined nine consumers across one
breaking-change minor in an afternoon.

**Extraction is hardest while the contract is still evolving.**
Locking in interface shapes early forces rework later. Doing it
post-hoc, after the contract has settled, produces a cleaner
result.

So the strategy is: stay monolithic, hold the line via discipline,
extract later when the costs flip.

## Discipline without extraction

We treat the plugin contract as if it were already a separate
module:

- **Contract surface is explicit.** Anything in
  `sparkwing/sparkwing` that plugins are expected to import is
  contract; everything else is internal.
- **`internal/` is used aggressively** for non-contract code. Go
  enforces that `internal/...` is private to the module tree, which
  prevents accidental contract leakage.
- **Contract changes are expensive on purpose.** Every breaking
  change to a contract type, signature, or verb gets a CHANGELOG
  entry and a migration recipe.
- **Runtime changes are cheap.** Anything inside `internal/` or
  non-contract packages can be refactored freely.
- **sparks-core is the canary.** It's the first plugin we have to
  migrate when sparkwing breaks something. Frequent breakage there
  means the contract surface is too unstable.

## What the extracted state will look like

When we do extract — likely as v1.0 prep — the target shape:

```
github.com/sparkwing-dev/sparkwing-sdk         (small, stable)
  contract types         Plan, Work, WorkStep, Job, Ref,
                         RunContext, Pipeline[T], Workable, Base,
                         NoInputs, ...
  DAG-construction       Job, JobApproval, JobSpawn, JobFanOut,
                         GroupJobs, Step, StepGet, ...
  Registry               Register[T], lookup APIs
  Convenience helpers    Bash, Exec, Info, IsDryRun, WorkDir, ...
                         (stdlib wrappers; don't pull in runtime)
  Runtime interfaces     Logger, RunContext methods, Cache backend

github.com/sparkwing-dev/sparkwing              (everything else)
  depends on sparkwing-sdk
  concrete impls of SDK interfaces
  DAG executor, scheduler, run lifecycle
  HTTP / dashboard / persistence / caching backends
  cmd/* binaries (sparkwing, wing, sparkwing-runner, ...)
  everything in internal/
```

The dependency arrow is one-way: plugins → SDK ← runtime. The SDK
depends on neither. When the SDK needs values from the runtime
(logger, run ID, dry-run flag), the runtime injects them via
`context.Context` keys defined in the SDK, or via interfaces the
SDK declares and the runtime implements.

A plugin's go.mod changes from `require .../sparkwing` to `require
.../sparkwing-sdk`; imports change in lockstep. For one or two
minor releases after extraction, the existing `sparkwing` package
re-exports SDK types as aliases so unmigrated plugins keep
compiling.

## When to extract

In rough order of importance:

1. Plugin authors crosses ~50 with significant external
   participation, where manual migration coordination breaks down.
2. The plugin contract has stabilized — three to six months
   without a breaking change.
3. We're committing to v1.0 within a release cycle.

If none apply, we don't extract. If two or more apply, we plan it.

## Path to v1.0

The expected trajectory, no specific dates:

1. **v0.2 → v0.5.** Iterate the contract. Breaking changes ship in
   minor bumps with migration recipes.
2. **v0.6 → v0.9.** Contract churn slows. Convention shifts toward
   "no breaks within a minor; additive changes only."
3. **Extraction.** sparkwing-sdk gets carved out. Aliases ease the
   transition.
4. **v1.0.0 cut.** sparkwing v1.0.0 + sparks-core v1.0.0 (all
   sub-modules) tagged together. The v1 compatibility promise
   above kicks in.
5. **v1.x.y maintenance.** Independent minor / patch bumps per
   module. Breaking changes wait for v2.
6. **v2.0.0 if and when.** Coordinated event. Migration tooling
   provided. v1 stays alive on its own path.

This is a plan, not a commitment. We may extract earlier if the
triggers arrive sooner; we may stay in v0 longer if the contract
isn't settling.

## What this means for plugin authors

**Today (v0 era):**

- Pin specific versions. Don't use `latest`.
- Treat sparkwing as unstable; read CHANGELOG.md before bumping.
- Watch for retracted versions. `go mod tidy` warns; the `retract`
  block in sparkwing's `go.mod` is the source of truth.
- Document the supported sparkwing version in your plugin's README
  ("compatible with sparkwing v0.2.x"). When sparkwing breaks,
  cut a new plugin version with the updated pin.
- For local development, use a `go.mod` `replace` directive
  pointing at a sparkwing checkout. Drop it before publishing.
- Don't expect API stability yet. We provide migration recipes for
  breaking changes; we don't promise zero-effort migrations.

**Post-v1.0:**

- Pin to the v1 line: `sparkwing-sdk v1`, `sparks-core/<sub>` v1.
  Specific minor / patch is up to you.
- API stability is real within v1. Patch and minor bumps are safe.
- The wing CLI binary is your interface to the runtime; install it
  once and your plugin only depends on the SDK module.
