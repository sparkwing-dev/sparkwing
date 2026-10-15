# W7 storage handoff

W7 remains incomplete. This session removes persistent Git mirrors from direct
source checkout. Earlier committed blob, artifact, memo, depcache, binary,
source-bundle and tagged-log work remains in place.

## Implemented and deleted

- Each direct fetch creates a standalone repository under the run checkout.
  It fetches from upstream even when an earlier run fetched the same commit.
  Exact SHA, branch and declared branch/tag selection still work.
- Removed mirror-key normalization, shared bare repositories, mirror locks,
  worktree registration/pruning, last-use timestamps, LRU eviction and idle
  mirror sweeping. Removed `SweepDirectMirrors`, the private checkout root
  argument and `GCStats.GitDirsRemoved`, then migrated their callers.
- Failed fetches remove only the checkout they created. Existing directories
  and symlinks are refused and preserved. Initialization ignores Git templates.
  Checkout keeps the existing config, filter, hook and credential fences.
- Kept the ten-minute fetch timeout and ten-GiB fetched-Git-data ceiling.
  The deleted twenty-mirror bound applied to the removed shared cache.
- Removed seven tests of the old mirror layout, reuse, locking and eviction,
  plus their unused helpers. Added seven isolated-checkout regression tests.
  Two regressions ran red against the original implementation: checkouts had
  linked Git metadata, and a second fetch succeeded with upstream unavailable.
- The baseline-before-pipeline fixture fetches a standalone repository with
  a controlled file transport before checking baseline adoption. It no longer
  preloads an otherwise unavailable upstream into the shared mirror.

The Go diff removes **248 net lines: 489 deleted and 241 added**, including the
new regression test file. This count excludes Markdown and generated mirrors.
The remaining old controller data/log runtime is still present.

## Verification

All Go commands use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `-p 2`, and
`GOCACHE`, `GOTMPDIR` and `TMPDIR` below `/tmp/sw-astra-W7`.
The lead can reproduce focused checks with the same environment and
`go -C <worktree>`.

Passed:

- Whole-repository `go build -p 2 ./...`.
- `go vet -p 2 ./internal/bincache ./internal/blob/... ./internal/artifacts
  ./internal/depcache ./pkg/storage/logseg ./internal/orchestrator
  ./internal/cluster`.
- Full tests for `internal/blob/...`, `internal/artifacts`, `internal/depcache`
  and `pkg/storage/logseg`.
- Focused bincache tests, ordinary and race runs, selecting
  `TestDirectCheckout(OwnsItsGitObjects|RefetchesFromUpstream|IgnoresGitTemplates|
  PreservesExistingDestination|SelectsUpstreamRefs|CancelsBeforeCreation|
  ConcurrentFetchesOwnTheirObjects|HardensTheUsersSSHCommand|
  RefusesUnsafeDeclaredRef)`, `TestAdoptWorkspaceBaseline*`,
  `TestValidateDirectSource*`, `TestDirectSSHCommand*`,
  `TestFetchPipelineSourceDirectRefuses*`, `TestWorkspaceBaseline*`,
  `TestCredentialRemote*` and `TestSSHCredentialNeedsATmpfs*`.
  Concurrent checkouts retain their Git objects after another checkout is
  removed. SSH credential tests use their existing private temporary roots.
- `TestGCWarmRoot*` ordinary and race tests.
- Windows/amd64 `go test -p 2 -c` for `internal/bincache`. Compilation only.
- Acceptance catalog, invariant and port-inventory checks. Storage enumeration
  reports **0 passing, 424 skipped, 0 failing target cells**. `acceptance.New`
  and `Run` remain stubs. These are no native product assertions.
- Documentation mirrors match their sources. Changed-line commentcheck and
  sleepcheck pass against HEAD, including the untracked regression file.
  `git diff --check` passes. Shared Git config has no test identity or
  `core.worktree` override.

`TestDirectCheckoutTakesTheBranchTipWithoutACommit` cannot run its HTTP fixture:
`httptest` is denied `listen tcp6 [::1]:0: socket: operation not permitted`.
That prevents a full bincache HTTP/HTTPS suite in this sandbox. No test was
skipped or changed to bypass that refusal. Standalone file-transport tests and
an SSH stand-in exercise real Git fetches and checkout without listeners.

New T1 inventory ports: **0**. No acceptance cell was unskipped without its
required native driver. The lead retains HTTP integration, native acceptance,
independent review and Sparkwing pipelines. Only the initial installed-CLI
discovery (`sparkwing info --for-agent`) ran before the brief was loaded. No
candidate Sparkwing execution, pipeline, daemon, Git write or release ran.

## Contract changes needed in other areas

No new frozen contract change is requested. The earlier gaps remain:

1. **W1: atomic upload lifecycle.** Define durable immutable ticket creation,
   commit/replay and expiry release. Commit must reauthorize the exact uploader,
   publish the live blob/receipt and settle unique-byte quota together.
2. **W1/W2/W9: working-tree submission.** Define the submitter's upload authority
   before a run exists and when execution may observe its sealed source digest.
3. **W1/W2: log signing projection.** Expose the immutable stored retention class
   with current writer authority. BatchSigner consumes that projection and
   lifecycle readiness without recomputing standing.
4. **W1/W2/storage: S3 log headers and replay.** The URL-only log contract lacks
   signed conditional-header transport and digest-verified replay/commit after
   uncertain PUTs. Future slots lack admitted size/digest metadata. A 412 cannot
   be accepted as proof of identical bytes.

## Real integration and remaining cuts

- W1 must implement RecordStore, read sets, memo-copy transactions, live blobs,
  upload tickets, unique-byte quota, retention and tombstones. Storage clients
  remain tested against their interfaces with fakes.
- W2 must bind capability-aware data/record routes and tagged log signing,
  commits and proxied reads. Reauthorize after bodies and at commit, and meter
  committed-read bytes on success and failure with the log-cap exemption.
  Wire S3 lifecycle/read methods and leased filesystem expiry.
- W1/W2 must enforce exact persisted writers, final-job grace, live ancestry
  and membership, and no newer claim. Eval retains its own lease rules.
- W3/W8 must provide fresh attempt bindings for artifacts, memo, depcache,
  binaries, source bundles and tagged logs, including retries and reconstruction.
  W4 must finish Cache integration and declaration linting.
- Replace/delete the old controller/client attempt-only signer, sealing,
  stream/ring authority and orchestrator log paths. Remove AttemptPrefix and
  legacy manifest fields after those callers move.
- Replace/delete old data handlers, cache grants and service-token accounting
  after final routes bind. Native acceptance drivers remain open.

## Shared files and assumptions

Runtime callers outside the storage implementation:

- `internal/orchestrator/warmgc.go`: remove the deleted mirror sweep and stat.
- `internal/cluster/runner_pool_cli.go`: remove the deleted stat's log field.

`internal/orchestrator/warmgc_test.go` drops mirror-only setup and assertions.
The bincache source/credential/submodule test callers drop the private root
argument. `internal/bincache/trusted_source.go` changes only the size-cap comment.
`internal/bincache/workspace_baseline_test.go` replaces mirror preloading.

Shared prose: `docs/storage.md`, `docs/local-execution.md`,
`docs/backup-restore.md`, `docs/migrations/_unreleased.md`, `CHANGELOG.md`, their
five generated mirrors, and this report.

The mirror cut follows the frozen architecture's direct-source replacement.
It assumes no prelaunch cache migration is required. Existing mirror directories
are left untouched. Undo is a revert of this cut. No new option, environment
variable, dependency, service, retry loop or customer limit was introduced.

Friction remains the HTTP listener prohibition, stubbed native acceptance,
and the cross-area contracts and bindings above. No new product question arose.
Scratch is removed at final handoff.

STATUS: CONTINUE
