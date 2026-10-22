# W3 captured-environment continuation

<!-- design:begin -->

W3 remains incomplete. This continuation implements private snapshot storage and
checks the sealed environment before source acquisition and native SDK startup.
The prior [amendment audit](w3-amendment-handoff.md) still applies to every W3 row.

## Implementation

`FileEnvironmentSnapshots` writes host-selected values to owner-only files through
`fssecure`. The file uses byte arrays because JSON strings replace invalid UTF-8.
Its identity comes from C4's `EnvironmentDigest`. Empty values survive. Duplicate
assignments fail, including case variants on Windows. Reads check file ownership,
permissions, type, the sealed names and the digest. Errors expose neither values
nor the private digest path. Reads leave the snapshot available to replacement
owners and to inherited child/retry requests. No age limit or retention policy
was added.

`OwnerHost` and `ClaimedNodeHost` load and verify the snapshot before the shared
source path builds Git/install environments. `SDKEnvironment` does so before
plan, attempt and eval startup. A supplied in-memory local base still needs the
sealed identity. A failed loader cannot return partial environment output or
select daemon values. Route revocation remains part of failed SDK startup.

`PlanningProcessInput.Environment` carries first-seal metadata through describe
and plan. Each invocation plans with the sealed snapshot and host environment.
A snapshot cannot hide missing or changed host inputs. Attempts and owner evals
load from the accepted request, including retries whose embedded source run ID
differs from the execution row. Filtering stays in `procenv.Build`.

Native child-process tests cover plan, attempt, eval and retry starts with real
private files and the real environment builder. They check captured values,
explicit empties, credential removal, sealed runtime identity and joined route
revocation. Source tests check that acquisition uses saved values after the
host's base changes. Changed snapshots fail before source or SDK work.

## Remaining contracts and integration

Two temporary overlay probes reproduced W6 failures without changing its files.

1. `procenv.Build(procenv.Git, ...)` returns `unknown process environment kind`.
   `Git` follows `HostChild` in the enum, but Build rejects every larger value.
   Source tests still inject the declared environment seam. Real default Git
   acquisition is blocked until W6 implements its declared policy.
2. `EnvironmentDigest` returns equal digests for maps whose VALUE strings contain
   byte `0xff` versus `0xfe`. JSON replaces both with the replacement character.
   W6 must hash native values without losing bytes. File storage preserves them,
   but digest verification cannot detect that substitution until W6 fixes hashing.
   This needs no new W3 API or customer limit.

The other contract requests in the amendment handoff remain open: complete
execution snapshots, terminal record references, owner policy transitions and
approval reads, cross-process mask propagation, and matched-engine host handoff.
Native intake, child submission, schedules and snapshot retention still need
W8/W9 assembly. W7's byte/record layers and authenticated process services remain
real integration points. No other area's contract or production file changed.

## Scope and deletion

No old-runtime lines were deleted in this continuation. The existing legacy
hosting cut remains blocked by the contracts above. No parked-test port was
claimed complete. Existing fixtures were updated to carry real snapshot seals.

Shared files changed in this continuation are `internal/acceptance/host_environment.go`,
`internal/acceptance/host_snapshot.go`, `tests/acceptance/skeleton_test.go`,
`c3-protocol-sdk.md`, `w3-amendment-handoff.md` and this document. All production
edits are in `internal/nodehost`. No caller in another production package needed
migration. The five merge-conflicted paths retain resolved contents. The index
still needs lead staging because workers cannot write git metadata.

## Verification

Commands use `GOWORK=off GOFLAGS=-buildvcs=false GOMAXPROCS=4`, with GOCACHE,
GOTMPDIR and TMPDIR under `/tmp/sw-astra-W3/`. Every test uses `-p 4` or fewer.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for nodehost, source, lease, orchestrator and both acceptance packages.
- Full source, lease and acceptance suites pass. Native acceptance target cells
  remain skipped. The new active cell is
  `TestEnvironment/environment-captured-local-base/hermetic-private-snapshots`.
- Snapshot and native environment/source tests pass, including race checks. The
  broad host selection from the amendment handoff passes with the added
  `TestCapturedEnvironment` and `TestNativeSDK` prefixes.
- Commentcheck still reports the inherited detached HereBearer comment in
  `internal/controllerwire/contracts.go:47`. Sleepcheck still reports the two
  inherited wall-clock checks in `internal/limits/deadline_test.go:58` and `:66`.
  Neither reports a W3 finding.
- `git diff --check` passes. No conflict marker remains. Git configuration still
  names the user's default identity and has no test-injected core.worktree.

The new behavior can be rerun with:

```text
go test -p 4 ./tests/acceptance -run '^TestEnvironment$/^environment-captured-local-base$/^hermetic-private-snapshots$'
go test -p 4 -race ./internal/nodehost -run '^(TestCapturedEnvironment|TestNativeSDK|TestSDKEnvironment|TestSDKPlanning|TestOwnerHostAcquires|TestClaimedNodeHostAcquires|TestOwnerHostRefusesChanged|TestClaimedNodeHostRefusesChanged)' -timeout 5m
```

Use the environment above and `go -C` with this worktree's absolute path. The full
legacy nodehost/orchestrator suites remain outside this pass because their
previously recorded store/schema and sandbox-socket failures are unchanged.
No native Windows/Darwin, Postgres, controller HTTP/api.sock or real-repository
QA claim is made. The lead retains those drivers and the Sparkwing pipelines.

The upstream probes fail with the two messages above. They are evidence of
unimplemented or incorrect foreign contracts, not passing integration checks.
Their overlay and all build/test caches are removed at worker shutdown.
No Sparkwing binary, pipeline, git write or external service was used.

<!-- design:end -->
