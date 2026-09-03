# Versioning and the plugin ecosystem

How sparkwing, sparks-core, and third-party plugins version
themselves, what compatibility you can expect, and the architectural
choices behind it. If you're authoring a plugin today, jump to
[**What this means for plugin authors**](#what-this-means-for-plugin-authors).

## Where we are today

Sparkwing ships as a single Go module:
`github.com/sparkwing-dev/sparkwing`. Plugin authors import
`github.com/sparkwing-dev/sparkwing/sparkwing` and get the entire
contract surface -- Plan, Job, Work, Register, Ref, the
DAG-construction verbs, plus convenience helpers (Bash, Exec,
Logger, etc.).

We are intentionally on the `v0.x.y` line. v0 has no semver
stability promise -- minors can break things, patches can introduce
new APIs. We are using v0's flexibility to iterate the contract.

A v1.x.y line on the Go proxy is **retracted** and unsupported -- do
not pin to it. The `retract` block in `go.mod` is the authoritative
list. v1.x snapshots stay resolvable (proxy snapshots are immutable)
but carry no support.

## Versioning per repo

Three repos participate in plugin compatibility:

- `sparkwing` -- SDK + runtime + CLI as one Go module.
- `sparks-core` -- first-party plugins, a Go module per top-level
  package (aws, docker, gitops, kube, s3, ...).
- Third-party `sparks-*` plugins -- independent Go modules with
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

## The SDK pin selects the CLI

`.sparkwing/go.mod` pins the SDK a repo builds against. That pin also
selects the CLI that runs the repo's pipelines, the way `go` honors a
`toolchain` line under `GOTOOLCHAIN=auto`. Before compiling a pipeline,
`sparkwing` compares its own version with the pin:

| Pin | Installed CLI | What runs |
|---|---|---|
| release tag newer than the CLI | release build | the pinned release, from the version store |
| release tag at or below the CLI | release build | the installed CLI |
| pseudo-version, or a `replace` for the SDK | anything | the installed CLI |
| anything | `(devel)` or a source build | the installed CLI |

A newer CLI needs no switch, because the daemon protocol serves older
pipelines. A source build on either side wins, because a checkout is
what its author is testing. Under `--sw-ref` the decision reads the
working tree's pin, not the ref's, because it is made before the ref's
worktree exists.

The version store is `$SPARKWING_HOME/toolchains/<version>/sparkwing`,
`~/.sparkwing/toolchains/<version>/sparkwing` by default. A fetch pulls
the release asset for this OS and architecture, verifies the Ed25519
signatures over the manifest and the asset plus the manifest's sha256
digest, asks the fetched binary for its own version and refuses to cache
it under a name it does not answer to, then installs it by rename, so two
concurrent hooks cannot exec a half-written binary. The signed
`SHA256SUMS` and `SHA256SUMS.sig` land beside the binary; a later run
re-checks that signature offline against the same release keys
`sparkwing update` trusts and compares the stored binary's digest against
the manifest entry, so a cache hit touches no network and anything that
does not check out is fetched again.

The pinned release hosts the admission daemon and migrates the shared
runs store in `$SPARKWING_HOME` exactly as the pipeline binary compiled
at that pin already does on every run; the
[schema requirements rule](deployment-modes.md#schema-versioning)
decides whether binaries at older pins keep opening it, which is why
`sparkwing repos update` moves the fleet together.

A switch is never silent. It prints one line to stderr and nothing to
stdout:

```text
sparkwing: running v0.40.0 from ~/.sparkwing/toolchains/v0.40.0/sparkwing because this repo pins SDK v0.40.0 and the installed sparkwing is v0.38.2
```

A fetch adds one line naming the release URL and the verified digest.
`sparkwing info` reports both versions whenever they differ, and
`sparkwing doctor` lists the version, path, and size of every release the
store holds.

### Running without the switch

`SPARKWING_TOOLCHAIN=local` forbids the fetch and the exec. A repo whose
pin outranks the installed CLI then fails, naming the version to install
by hand:

```text
this repo pins SDK v0.40.0 but the installed sparkwing is v0.38.2 and SPARKWING_TOOLCHAIN=local forbids fetching one. Install v0.40.0 with `sparkwing update --version v0.40.0`, or unset SPARKWING_TOOLCHAIN
```

A fetch that cannot reach the release host fails the same way, naming the
URL it tried. Neither case falls through to the installed CLI, because
that is the build the pin already ruled out.

`SPARKWING_TOOLCHAIN=auto` is the default and the only other accepted
value. The CLI a switch starts runs with `SPARKWING_TOOLCHAIN_ACTIVE` set
to the version it was chosen as, so it does not switch again for that
pin; it clears the variable from its own environment, so the pipeline
binary and the daemon it starts do not carry the guard on to another
repo. A repo pinned to some other release down that tree still switches.

On unix a switch replaces the process. On Windows it runs the pinned CLI
as a child and exits with its status.

### Reclaiming the store

Nothing prunes the store. Each release a repo pins leaves one CLI binary
under `$SPARKWING_HOME/toolchains/`, around 60 MB each. `sparkwing
doctor` lists the version, path, and size of every entry; delete
`$SPARKWING_HOME/toolchains/<version>` to reclaim one. The next run that
needs it fetches it again.

### How this relates to the update commands

- `sparkwing update` replaces the CLI on your PATH. Run it to move this
  machine's default forward.
- `sparkwing repos update --apply` moves the pins: it bumps every tracked
  repo's `.sparkwing/go.mod` to a target release and commits the result,
  so the fleet lands on one version.
- The version store is neither. It holds the releases individual repos
  ask for while their pins sit ahead of your PATH. Fetches stop once
  `sparkwing update` raises the installed CLI to the highest pin, or
  `sparkwing repos update --apply` lowers the pins onto it, and the store
  is safe to delete once they agree.

## The daemon's wire surface

The local admission daemon serves two surfaces: the wingwire protocol on
its socket, and the controller HTTP API. Both grow by default and are cut
on purpose.

- A message type, field, route, or response member a released daemon has
  served stays served. New fields arrive with `omitempty` on the wingwire
  side and as optional members on the HTTP side, and their zero value
  means what the field's absence meant before it existed.
- Each side ignores fields it does not know. After the handshake, a
  message type the daemon does not know or does not serve is answered with
  `unsupported`, naming the type, and the connection continues, on a
  health-probe connection as on any other. A health probe may only read
  queue state, so `unsupported` there means "not served on a health probe"
  rather than "unknown to this daemon". Before the handshake the daemon
  answers `unsupported` and then hangs up, because there is no session to
  continue. The eighth refusal on a connection is its last: the daemon
  sends it and closes. The type name in the reply is the peer's own,
  truncated to 64 bytes, and a frame carrying no type at all is malformed
  rather than unknown, so it ends the connection with no reply. An
  unregistered controller route answers 404 with
  `{"error":"unsupported","route":"<method> <path>"}`. None of these is a
  dropped frame the caller has to time out on.
- A cut is a release decision. Removing or retyping a message field,
  removing a route, method, parameter, or response member, or raising the
  protocol floor needs a `(Breaking)` changelog entry under a wire scope
  (`wingd`, `wingwire`, `wire`, `api`, `controller`, or `cache`) that
  names what was cut, and a migration section that the entry links.

The rule is held up mechanically. `pkg/wingwire/testdata/shapes.json` records
every message type and field by JSON name and Go kind, generated by
reflection over the registered set; a test regenerates it and fails on any
difference, separating what was removed or retyped from what was added.
The release pipeline's `gate-wire-changelog` job then diffs that snapshot,
`api/openapi.yaml` (routes, methods, parameters, and response members), and
the protocol constants against the previous tag. It refuses to cut a
release unless every entry the diff names is spelled out in the breaking
changelog entry or in the migration section that entry links. Naming the
route, the operation, or the message type covers everything beneath it, so
a release note reads as prose rather than as a list of document paths.

Raising the floor is a warning boundary rather than a failure boundary. A
pipeline binary whose pin speaks a major below the floor will run
standalone: it will say so once on stderr, run against a store of its own
that `sparkwing runs` and the dashboard do not see, and exit as it would
have. Age is never a reason to refuse a run, because pipelines run as
commit hooks and crons, where nobody wants to be told to upgrade.
`sparkwing repos update --apply` moves the pins that are behind.

The floor moves while sparkwing is pre-1.0, whenever carrying an old
generation costs more than the cut is worth, and cuts are bundled into one
minor release so an old pin sees a single warning period. At v1.0 the
floor freezes: after that, every generation the daemon has served stays
served.

## Compatibility coordinate (post-v1)

When sparkwing reaches v1, we plan to lean on Go's path-encoded
major version as the cross-repo compatibility signal:

| Era | sparkwing path | sparks-core path | Compat rule |
|---|---|---|---|
| v0 | `.../sparkwing` | `.../sparks-core/<sub>` | none -- pin specific versions |
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
plugin contract out of the runtime into a separate module -- call it
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

**The plugin ecosystem is still small enough for migration tooling
to cover.** A breaking minor is rebaselined across the first-party
consumers in a single pass; extraction starts paying for itself only
once coordinating that pass stops being tractable.

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

When we do extract -- likely as v1.0 prep -- the target shape:

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
  cmd/* binaries (sparkwing, sparkwing-controller, sparkwing-runner, ...)
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
2. The plugin contract has stabilized -- three to six months
   without a breaking change.
3. We're committing to v1.0 within a release cycle.

If none apply, we don't extract. If two or more apply, we plan it.

## Path to v1.0

The expected trajectory, no specific dates:

1. **Early v0.** Iterate the contract. Breaking changes ship in
   minor bumps with migration recipes.
2. **Later v0.** Contract churn slows. Convention shifts toward
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
- The sparkwing CLI binary is your interface to the runtime; install it
  once and your plugin only depends on the SDK module.
