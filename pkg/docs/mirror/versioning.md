# Versioning and the plugin ecosystem

How sparkwing, sparks-core, and third-party plugins version
themselves and what compatibility you can expect. If you're authoring a
plugin, jump to
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

The `retract` block in `go.mod` is the authoritative list of versions
this project does not stand behind: the whole v1.x.y line, and v0.52.0,
a tag cut from a fixture commit on no branch and recalled within the
hour. Do not pin to any of them.

Go reads a module's retractions from its `@latest` go.mod, which the
proxy resolves from the repository's live tags. Every entry in the block
is in force from the first release that carries it: `go list -m
-versions` omits every retracted version, and `go list -m -retracted
-versions` shows them again. A retraction reaches consumers with the
next ordinary v0.x release whose `go.mod` carries the line, and needs no
release on the v1 line to deliver it.

Retracted and resolvable are separate things. The proxy keeps serving a
version it fetched at least once, so v0.52.0 and v0.30.0 still resolve;
the v1.x tags were never fetched through it and their `.info` and `.mod`
endpoints answer 404, though the version list still names them. A
version the proxy holds is never re-cut on another commit: a second tag
under that version would mismatch the `go.sum` every consumer has
already cached.

Version tooling reads the version list rather than the retract block
until a release carries the line, so a release that outranks v0.52.0 is
what stops tooling offering the recalled tag as an upgrade. That is a
separate recommendation from the retraction itself.

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

`.sparkwing/go.mod` pins the SDK a repo builds against. A foreground
`sparkwing run` also uses its direct stable pin to select a CLI, the way
`go` honors a `toolchain` line under `GOTOOLCHAIN=auto`. With the default
`SPARKWING_TOOLCHAIN=auto`, it compares the installed version with that pin:

| Pin | Installed CLI | What runs |
|---|---|---|
| release tag newer than the CLI | release build | the pinned release, from the version store |
| release tag at or below the CLI | release build | the installed CLI |
| pseudo-version, prerelease, or a `replace` for the SDK | anything | the installed CLI |
| anything | `(devel)` or a source build | the installed CLI |

An older pin keeps the newer CLI. Admission protocol negotiation can
serve an older handshake, but execution and output APIs must also remain
compatible. A documented v0 breaking change can require an SDK update;
for example, v0.66 output-producing pipelines require the object-output
contract. Selection does not fetch an older CLI to match an old pin.

Source builds stay selected because the checkout is what its author is
testing. Selection reads the direct working-tree requirement, not the
version Go's MVS resolves from the whole dependency graph. Under `--sw-ref`
it also reads the working tree's pin before the ref's worktree exists.

Detached submissions, compiled schedules and directly launched runner
executables do not use this foreground selector. The selected cached CLI
does not replace the installed executable on PATH, update resident runners
or deploy a remote controller.

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

A selected CLI can start a successor daemon and migrate the shared runs
store. Daemon succession preserves active leases so compatible pipelines
can reconnect; it does not stop every old agent, consumer or schedule.
The [schema requirements rule](deployment-modes.md#schema-versioning)
decides whether older direct database clients can reopen the store. It
does not translate incompatible HTTP output requests.

For a breaking store or execution change, follow its migration guide and
drain the affected old writers before upgrading. Moving SDK pins with
`sparkwing repos update` is one part of that coordinated upgrade.

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

Network failures and release signature or digest failures stop selection.
If the requested release's assets have not been published, the selector
announces and uses the latest published release if it precedes the pin.
If none does, selection fails. If the selected release is already installed,
it continues without re-exec. This fallback does not
establish that every feature of the newer SDK works on that release.
`SPARKWING_TOOLCHAIN=local` still refuses a newer stable pin.

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

## What this means for plugin authors

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
