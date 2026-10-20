# W9 burst claim cache and shell metadata privacy

Base: `1a4c4ff90`. The latest lead-native report contains no FAIL and passes
both detached and attached profile-cache tests. It predates this session's
changes. The lead must rerun the native family after committing them.

The merge's four file-content conflicts are resolved. C1 retains the integration
branch's committed retry-selection contract and W9's verified-default/source
contracts. The host fixture carries `IntakeRequest` through admission while
retaining the account authority required by burst. Controller Retry forwards
the amended mode through `admitRun`. Its transition adapter retains publication
results and masked-error validation. The store's clock comment retains the same
fixture-only semantics. No integration-deleted file was restored.

The index still lists the four files as unmerged because the brief forbids Git
writes. The lead must stage `design/rewrite/contracts/c1-data.md`,
`internal/nodehost/store_host_fixture_test.go`,
`internal/controller/engine_lifecycle.go` and `internal/store/store.go`, then finish the
merge. No commit, rebase, reset, stash or Git environment override ran here.

The cache invariant is that every local execution with a sealed cache profile
receives that profile's reader before its first step, regardless of which
claimant wins. The queue can claim ready jobs independently of the owner.
`newLocalNativeServices` previously opened the profile cache only for trigger
awards. A separately claimed node therefore started without that cache. The
constructor now binds it for both award types. Publication stays on the owner.
The existing fresh-attempt storage adapter retains this cache under the sealed
local opt-in.

`TestLocalQueueCacheDoesNotDependOnOwnerWinningJobClaim` substitutes the native
process-service constructor, retaining the real queue composition, private
client, SQLite request read and profile credential client. It forces both award
types without a scheduling delay. Before the fix, the node case fails with
`bound=false issued=0 revoked=0 closed=1`. Afterwards both cases pass, including
reader revocation and native-service cleanup. The source native test has no
fixed sleep. Its Cloud seed finishes synchronously before record verification,
and detached publication already waits on a completion notification. No test
sleep, timeout or retry was added. This reproduces a claim-order cache defect.
The unavailable original native failure output prevents claiming it proves the
cause of that particular failed run. The lead's native rerun remains required.

The privacy invariant is that Cloud uploads contain no names or digest from a
requester's private shell snapshot. Local capture fills `Env.Names` in
`FileEnvironmentSnapshots.Capture` through `capturedEnvironment`, then
`prepareLocalRunRequest` carries that snapshot to the local controller. Those
local facts remain necessary for detached execution and retries.
`PublishLocalRun` copied them into both the uploaded request and resolved spec.
It now omits the private snapshot from both, preserving the local source state.

Burst preparation calls `prepareTriggerRequest` with an empty environment map.
`EnvironmentDigest` therefore emits no shell names. No burst production change
was needed. Its existing real-intake test now sets a stray shell variable and
asserts that the persisted Cloud snapshot contains neither that name nor any
environment names. Both native publication modes check the same boundary.

The real router/SQLite/signed-object publication test failed before the fix with
`private shell name reached Cloud history: CLAUDE_CODE_MESSAGING_TOKEN` for both
success and failure history. It now passes with that name, AWS_PAGER, KUBECONFIG
and WSL_DISTRO_NAME. It checks both request and accepted spec, excludes the local
digest, and verifies that publication leaves the local snapshot unchanged.
`FuzzProfilePublicationExcludesShellEnvironment` generates additional shell
names through the same persisted Cloud boundary.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9 and
GOTMPDIR=/tmp/sw-astra-W9/go, with at most four Go workers per command.

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for cmd/sparkwing, controller, controller/client, nodehost and store.
- Focused local queue, profile-cache, local-run, burst intake/fallback and
  publication tests pass.
- Full short controller/client and doccheck suites pass.
- Retry tests pass in nodehost, controller and store after merge resolution.
- Acceptance `TestSurface*` and
  `TestDataRemoteCacheForcesVerifiedDefaultLookup` pass. The nodehost
  `TestFreshAttemptStoragePreservesOnlySealedProfileCache` regression passes.
- Commentcheck against main passes across 2,750 files. Sleepcheck against HEAD,
  embedded-document synchronization and `git diff --check` pass.

Initial race/native-tag compilation and fuzz vet encountered missing imports
under the shared Go cache. For example, crypto/rand and sync/atomic cache files
were unavailable during import. The fuzz process itself passed four executions,
but vet made that command fail. The cache was retained. Dedicated vet had
already passed. Follow-up checks disable vet to isolate execution from that
failure. The follow-up `-vet=off` race selection passes in cmd/sparkwing and
controller. The follow-up publication fuzz passes seven executions in six
seconds. Neither check required a source change after its cache failure.
The follow-up native-tag compile passes with `-vet=off -tags rewrite_integration
-run '^$'` for cmd/sparkwing and controller. The embedded docs content and privacy
guards pass. No native test was executed by that compile check.

Contract changes made: C4 records claim-independent local profile binding and
shell-snapshot omission during publication. Contract changes needed: none.
The lead-owned amendment and handoff files were read and left unchanged.
Existing W9 amendment implementations were retained. No archived test was
newly ported, no acceptance cell was unskipped, and no legacy subsystem was
replaced in this bounded correction.

W9 production Go this session: **+20/-13, net +7**. The removed lines are the
replaced owner-only cache branch and constructor shape. Whole legacy files
deleted: **0**. W9 production versus main is **+19,033/-23,818, net -4,785**,
within the -2,900 budget. Tests and contract declarations are excluded.

Shared files touched beyond W9 ownership are the four merge-resolution files
listed above and `internal/controller/publication_http_test.go`. W9 changes are
`cmd/sparkwing/local_host.go`, the new `local_profile_queue_test.go`,
`burst_intake_unix_test.go`, `burst_cli_integration_unix_test.go`,
`internal/controller/client/publication.go`, C4, local-execution.md and its generated
mirror, and W9's own report/handoff. No removed API needed caller migration.

The lead must rerun `^TestBurstPublicRun` in cmd/sparkwing and
`^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$` in internal/controller,
both with `-tags rewrite_integration`. Native sockets, deployed Cloud/DinD,
cross-machine transfers and platform/database QA remain lead evidence. No new
architecture decision or foreign implementation stub blocks this correction.
Friction was missing shared-cache imports and the sandbox's native socket
boundary. Scratch was removed after all checks joined. The shared cache is kept.

STATUS: CONTINUE
