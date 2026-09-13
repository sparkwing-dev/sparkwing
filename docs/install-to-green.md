# Install to green

The demo claim is that a machine with no sparkwing on it reaches a green
pipeline run in **under sixty seconds**. `bin/install-to-green.sh` is the
measurement behind the claim: it installs the CLI the way an adopter does,
scaffolds a pipeline, compiles it, runs it, and prints the seconds from install
to first green with each phase broken out.

## What it measures

Every run starts from a HOME the harness creates, so the Go module cache, the
Go build cache and the sparkwing home are empty. The number therefore includes
the dependency download an adopter waits for on a machine that has never built
a sparkwing pipeline.

| Phase | Command | What it covers |
| --- | --- | --- |
| install | `install/install.sh` | signature and digest verification, placement, `sparkwing info --first-time` |
| scaffold | `sparkwing pipeline new` | the pipeline module, its source file, and module-graph resolution |
| compile | `sparkwing pipeline explain` | the first build of the pipeline binary, including every dependency download |
| run | `sparkwing run` | dispatching the scaffolded job to a green finish |

The harness reads the run's terminal record and refuses to report a number
unless that record says the run succeeded.

## Running it

```bash
bash bin/install-to-green.sh                       # build this checkout, stage it, measure
bash bin/install-to-green.sh --output json         # one record for a script to parse
bash bin/install-to-green.sh --binary ~/.local/bin/sparkwing
bash bin/install-to-green.sh --release             # the published install path, over the network
bash bin/install-to-green.sh --target-seconds 60   # exit non-zero when the total is over target
```

Without `--target-seconds` the harness reports the number and fails only when
the demo path does not reach green, so a loaded machine records a slow
measurement rather than a red check. `pre-release` runs it that way on every
release candidate and logs the JSON record.

Local modes stage the asset on disk and serve it to the installer over
`file://`, so their install phase measures verification and placement rather
than the network transfer. `--release` installs from the published release and
includes the download. A candidate build is stamped with the checkout's newest
release tag, because the scaffold pins the SDK at the version the CLI reports
and the module proxy has to be able to serve that version.

The harness needs `go`, `git`, `curl`, and an OpenSSL 3 that can verify ed25519
signatures. It stops the admission daemon it started and removes its scratch
tree.

## Recorded measurements

Each row is one run on a 16-core Linux host (WSL2, 23 GB), with the one-minute
load average the host reported as the run started. Phase columns are seconds.

| Date | Mode | sparkwing | Load | Total | install | scaffold | compile | run |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-09-13 | binary | v0.50.1 | 30.9 | 45.2 | 0.7 | 9.1 | 35.0 | 0.4 |
| 2026-09-13 | binary | v0.50.1 | 25.9 | 46.4 | 0.6 | 10.5 | 34.8 | 0.4 |
| 2026-09-13 | build | v0.50.1 | 26.4 | 56.8 | 0.9 | 12.1 | 43.5 | 0.4 |
| 2026-09-13 | build | v0.50.1 | 19.6 | 65.6 | 0.6 | 10.3 | 54.2 | 0.6 |

The first compile dominates every measurement. It is the SDK's whole dependency
tree built from source into an empty build cache, so the lever that moves the
number is what a scaffolded pipeline has to build before its first job runs.
Scaffold and compile both scale with machine contention, and every row above
was taken with other builds holding the host above a load average of nineteen
on sixteen cores. Read a row against its load column before reading it against
the target.
