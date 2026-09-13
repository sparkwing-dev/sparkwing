# Install to green

The demo claim is that a machine with no sparkwing on it reaches a green
pipeline run in **under sixty seconds**. `bin/install-to-green.sh` is the
measurement behind the claim: it installs the CLI, scaffolds a pipeline,
compiles it, runs it, and prints the seconds from install to first green with
each phase broken out. `bash bin/install-to-green.sh --help` lists its flags.

## What it measures

Every run starts from a HOME the harness creates, so the Go module cache, the
Go build cache and the sparkwing home are empty and the number includes the
dependency download an adopter waits for. Module resolution is pinned the same
way: `GOPROXY` is reset to the public default and an inherited `GOFLAGS`,
`GOTOOLCHAIN`, `GOMAXPROCS` or checksum-database override is reset and named in
the output, so two runs of the same commit resolve the same graph.

| Phase | Command | What it covers |
| --- | --- | --- |
| install | `install/install.sh` | signature and digest verification, placement, `sparkwing info --first-time` |
| scaffold | `sparkwing pipeline new` | the pipeline module, its source file, and module-graph resolution |
| compile | `sparkwing pipeline explain` | the first build of the pipeline binary, including every dependency download |
| run | `sparkwing run` | dispatching the scaffolded job to a green finish |

The record carries what makes a number readable later: the phases, the
dominant one, the start timestamp, the core count, the load average at the
start and at the end, the effective `GOPROXY`, the mode, and which SDK source
the scaffold compiled against.

## What each mode installs

`--build` builds `cmd/sparkwing` from the checkout, stamps it with the newest
published `vX.Y.Z` tag (prerelease and local candidate tags are excluded, so a
tag nothing has published cannot be selected), and points the scaffolded
module at the worktree with a `replace` directive. The compile phase therefore
measures the branch's own SDK. A build row is a branch source build wearing the
tag it reports, not that release.

`--binary PATH` stages an existing binary under the version it reports, and the
scaffold resolves that released SDK from the proxy.

Both local modes serve the staged release to the installer over `file://` and
replace the installer's trust root with an ed25519 key the harness mints, so
the install phase measures verification against that key rather than the
release signing key, and measures placement rather than the network transfer.

`--release` runs `install/install.sh` unmodified against the published release,
with the shipped trust root, and its install phase includes the download.

## What each outcome means

A run that reaches green prints the record and exits zero. A demo path that
does not reach green exits 1. A module proxy the harness cannot reach exits 75,
because nothing was measured and a download failure is not a slow demo path.
`pre-release` runs the harness on every release candidate without a target, so
it logs the record, records status 75 as a skipped measurement, and fails only
on a demo path that never reached green. Pass `--target-seconds N` to turn the
target into an exit status.

The harness stops the admission daemon it started, removes its scratch tree,
and clears the git binding variables it inherits, so a run from a git hook or
`git bisect run` commits into its own scratch repository rather than the
caller's.

## Measurements

Append a row per recorded run; rows are never edited. Each is one run on a
16-core Linux host (WSL2, 23 GB) with the one-minute load average the host
reported as the run started. Phase columns are seconds.

| Date | Mode | sparkwing | Load | Total | install | scaffold | compile | run |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-09-13 | binary | v0.50.1 | 30.9 | 45.2 | 0.7 | 9.1 | 35.0 | 0.4 |
| 2026-09-13 | binary | v0.50.1 | 25.9 | 46.4 | 0.6 | 10.5 | 34.8 | 0.4 |
| 2026-09-13 | build | v0.50.1 | 19.0 | 65.4 | 1.4 | 16.1 | 46.9 | 1.0 |
| 2026-09-13 | build | v0.50.1 | 30.2 | 59.7 | 0.6 | 11.0 | 47.2 | 0.8 |
| 2026-09-13 | binary | v0.50.1 | 25.3 | 65.6 | 1.0 | 12.7 | 51.4 | 0.5 |
| 2026-09-13 | binary | v0.50.1 | 36.7 | 61.0 | 0.9 | 10.7 | 49.0 | 0.4 |
| 2026-09-13 | binary | v0.50.1 | 13.6 | 57.0 | 0.4 | 10.2 | 45.2 | 1.1 |

The first compile dominates every row. It is the SDK's whole dependency tree
built from source into an empty build cache, so the lever that moves the number
is what a scaffolded pipeline has to build before its first job runs.

The rows above span 45 to 66 seconds on one host in one afternoon, and the load
column does not order them: load average counts runnable tasks, not the CPU
share the compile actually gets, so a host busy with sixteen-way parallel test
binaries compiles slower than the same load average made of idle-waiting work.
Read a row as one observation with its context attached, not as the host's
number, and compare rows taken on an otherwise idle machine when comparing
against the target.
