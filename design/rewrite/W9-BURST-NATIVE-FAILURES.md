# W9 native burst fixes

Starting HEAD: `1de21d73b`. No Git writes, installation, deployment or release.
Lead-owned handoffs and amendments remain unchanged. Earlier completed work was
not rebuilt. This continuation addresses every failure in the lead's native
burst report.

## Fixes and evidence

- **Enrollment 401:** reproduced through the actual session route. The native
  fixture created account sessions without the signed multi-team license that
  enables them. Added the license and the plain-HTTP cookie configuration to
  that fixture. Its new socket-free session-client enrollment test passes.
  The CLI already presents Session authorization and the selected session's
  CSRF proof. Production credential requirements are unchanged.
- **Here handoff:** a real intake/claim regression reproduced the CLI rejecting
  the controller's `PurposeTrigger` execution award. `PurposeHere` reserves the
  named run before claim. Hosting now requires the actual execution award plus
  its sealed here selection. Updated the invented award in the unit fixture.
  The observing member also needed the documented `logs.read` grant.
- **Interrupt argument:** the fixture's `--block` is a pipeline argument. The
  documented grammar requires `run burst -- --block`. Fixed the invocation.
  Added no CLI flag. Runner cleanup retains its existing process-exit bound
  through a cleanup context instead of a direct timer wait.
- **Offline fallback assertion:** the command succeeded but omitted the SDK
  markers because attached local hosting never started a log follower. Local
  construction now supplies the existing controller log follower, and attached
  hosting joins it with the owner through the existing observation function.
  Detached receipts retain their existing path. The streaming regression passes.
- **Cache warnings:** real-store tests reproduced the unconditional rejection
  of local repository caches. Verified local execution now uses its own sealed
  checkout namespace. The namespace and client also preserve an absent,
  unverified Git ref. Save and lookup pass with and without a branch. The native
  fallback assertion now also refuses restore/save warnings. Corrected the
  obsolete local-cache path in the public caching guide and synced its mirror.

Invariant: only a live execution award for the selected run can reach here
hosting. A new fuzz test generates holders and generations and refuses
reservation credentials and foreign run fences. The real HTTP-handler claim
is also checked through the CLI's host-input validator.

Invariant: local dependency caches remain bound to verified local execution and
its repository, ref and attempt. A generated property refuses account/machine
subjects, remote-cache/publication/expansion uses and foreign repositories.
Real-store producer/consumer round trips retain receipt and read-edge checks
and refuse generated foreign-repository requests. A namespace fuzz
property keeps absent, named and verified refs distinct. Existing adversarial
client scope-substitution tests remain in force.

## Contract changes made

- C1: `SubjectLocal` plus `DataClaim` may read/write dependency caches under
  its sealed `local:` repository. Shared memo restrictions remain. Empty
  unverified refs have a distinct namespace component. Empty verified refs fail.
- C3: here reservation authority and the awarded trigger execution credential
  are distinct. The client accepts the latter for hosting. No wire field changes.
- C4: attached local construction requires the shared log follower and joins it
  with hosting. No route, grant, schema, flag, timeout or environment variable added.

## Retry-selection DESIGN handed back to the lead

The invariant is that retry selection belongs to committed execution intent
while source seals remain immutable. `TestReviewRetryModeIsPartOfCommittedReplay`
still fails: changing `failed` to `all` under the same key returns the earlier
execution with `replayed=true`. `EngineController.Retry` validates Mode and then
drops it before CreateRun. I recommend one C1 admission-owned retry selector:
C2 passes the normalized mode separately from the source seal, C1 commits it
and the selected source result fences in the new execution's lineage and
includes canonical source ID plus mode in the idempotency digest, and W3
consumes that selection after restart. Preserve the source request/spec hashes.
Require changed-mode conflicts, exact alias replay and failed/all execution
proofs. This is explicitly handed back for the lead to assign across C1/C2/W3;
it is not closed or patched with another CLI special case.

## Verification

Commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go, TMPDIR=/tmp/sw-astra-W9 and at most four package
workers.

Passed: whole-repository build; vet of CLI, client, blob and store; complete
short blob/client suites; focused CLI burst, here, owner-composition, enrollment
and local-run suites; controller burst/native-SDK/data selections; store burst,
local-cache, metadata and record selections; nodehost/localcontroller cache
selections; W9 surface, here and credential-policy acceptance selections;
commentcheck against main; sleepcheck against HEAD; doccheck; embedded-doc drift
and whitespace checks. Race checks pass for attached logs, local cache
producer/consumer reads, here-award fences, namespace separation and adversarial
client scope decoding. Three-second fuzz campaigns pass: 1,431 here-award
executions and 37,458 namespace executions.

No acceptance cell was newly unskipped, and no archived test was newly ported.
The new regression/property tests are listed above. The native probes compile,
then skip because this sandbox refuses loopback and local-controller sockets.
Those skips prove no native cell. The retry replay test fails as handed back.

The lead must rerun these exact native commands with sockets available:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  TMPDIR=/var/tmp/sw-lead-tmp GOTMPDIR=/var/tmp/sw-lead-tmp \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  TMPDIR=/var/tmp/sw-lead-tmp GOTMPDIR=/var/tmp/sw-lead-tmp \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./pkg/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' \
  -count=1 -v -timeout=5m
```

Real deployed Cloud execution still needs the shipped runner/DinD Job, source
service, object backend and network boundary. The existing fake-Kubernetes
proof is component evidence. Fresh-machine release distribution, native cache
composition, Darwin/Windows and PostgreSQL remain lead integration lanes.
Requester-secret restart loss and one-replica topology remain documented.
Cross-profile memo/provenance refusal stubs are outside these burst fixes and
remain integration work, as recorded in earlier reports.

## Size and shared files

This session deletes 6 production lines and adds 23, net +17. The removed paths
are the reservation-purpose expectation, direct attached hosting without log
observation, unconditional local-cache refusal and empty-manual-ref refusal.
No replacement subsystem or compatibility path was added. W9-owned production
is +14/-4, net +10 this session. W9 against main is +18,445/-23,824, net -5,379,
within its -2,900 budget. Tests, testdata and contract declarations are excluded.
No API was removed, so no cross-package caller migration is needed.

Shared files touched: internal/blob/keys.go and authority_test.go;
pkg/store/blob_lookup.go, blob_persistence.go, blob_record_runtime.go and
blob_local_cache_test.go; C1/C3/C4 contracts; CHANGELOG.md and its generated
pkg/docs/changelog.md; docs/caching.md and its generated mirror. All other code
changes are W9-owned CLI/client files.

Mechanisms added: one required local log-follower binding reuses existing
observation and transport. A direct host call cannot stream committed logs.
Local cache eligibility reuses authenticated subject/data use and the existing
store namespace. No new store, cache service, retry loop or public setting.
Friction: socket refusal and the installed standards tool's reported source lag.
Native completion and independent review remain with the lead.

Git identity and core.worktree overrides remain absent. All checks have joined.
W9 scratch was removed. The shared build cache was retained. No merge.

STATUS: CONTINUE
