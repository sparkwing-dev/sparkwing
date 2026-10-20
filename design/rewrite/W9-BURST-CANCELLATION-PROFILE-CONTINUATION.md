# W9 burst cancellation and profile continuation

Base: `73c7b8263`. No Git writes. Retry selection, lead-owned handoffs and
amendments are unchanged.

## Implemented

The owner now waits for a remote holder whose cancellation is already persisted.
It previously submitted another cancellation on every scheduling pass. The
scheduler regression ran red with `trace=[renew acquire start cancelled finish]`,
then passed with the remote completion observed before owner finish.

A real-router regression also reproduced a refusal on the owner's first
cancellation of a started job: HTTP 409, `state transition refused`. Its result
proof used the owner's zero attempt, while the store compared it with the job
holder's positive attempt. Non-node proofs now use their issuing capability's
attempt. This replaces the publication-only exception with one rule for owner
and publication proofs. Node-holder proofs still use the captured source
ordinal checked against the persisted job.

Security invariant: cancellation requires the current issuing authority and its
exact proof, and cannot change another holder's attempt, generation or claim.
The new seeded property generates valid proofs and mismatched ordinals, jobs,
generations and actors, plus expired leases and revoked authority. It checks
refusals without state changes, exact replay and preservation of holder rights.
Both the router test and property's valid cases ran red before the correction.
They pass afterward. Existing holder retry-ordinal and result-replay tests pass.

The native profile-cache fixture passed Git's trailing newline as part of the
symbolic ref. The native failure names that invalid ref, and runSnapshotGit
returns raw stdout. The fixture now trims branch and SHA metadata. Product Git
behavior and the profile surface are unchanged. The native outcome assertion
now includes the CLI's captured stdout/stderr, so a failed host names its cause.

## Profile construction and publication

A1-50's ordinary-account profile credential construction, verified-default cache
reader, one-way publication, temporary credential cleanup, and attached and
detached host bindings remain implemented. Real-router/store/signed-filesystem
tests pass for credential narrowing, revocation, publication of results and logs,
and profile reads of verified default output, manifests and artifact bytes.
No replacement implementation or test-only product route was added.

The lead's latest native run already passes attached and detached publication.
Its attached and detached profile-cache probes stop at the invalid Git fixture
before local execution. Those probes need the corrected fixture's native rerun.
The W9 handoff now points here and removes its stale claim that profile
construction and publication remain unimplemented.

## Contracts and volume

Contract changes made: C1/C2 distinguish the issuing proof's attempt from the
targeted holder's attempt. C3 records waiting on persisted cancellation intent.
Contract changes needed: none. Existing assigned amendments and local-integrator
handoffs retain their earlier implementations. A1-50 native cache evidence and
A1-53 native/deployed evidence remain open.

Production Go versus HEAD: **+4/-1, net +3**. One publisher-specific condition
was replaced, and three lines avoid a redundant cancellation request. No file,
API, legacy subsystem or archived test was removed this session. No caller
migration was required. Burst-named production files are unchanged. W9-owned
production versus main remains **+19,011/-23,818, net -4,807**, within -2,900.
This continuation adds three lines to the existing burst production delta.

Shared files: `internal/nodehost/{owner_schedule.go,owner_test.go}`,
`internal/store/{result_proof.go,cancellation_intent_runtime_test.go}`,
`internal/controller/private_host_test.go`, and contracts C1/C2/C3. W9 changes its
native CLI test and own handoff/report. No public API shape, flag, environment
variable, schema, service, limit or retry-selection rule changed.

No docs impact on the product surface: the fixes restore the existing documented
first-interrupt cancellation drain. Contract prose changes with the code.

## Evidence

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`TMPDIR=/tmp/sw-astra-W9`, `GOTMPDIR=/tmp/sw-astra-W9/go`, and `-p 4` at most.

Passed:

- Whole-repository `go build -p 4 ./...` and focused vet over CLI, nodehost,
  controller, client and store.
- Owner cancellation/wait/authority-loss regressions, also under `-race`.
- Store cancellation/proof/holder tests and controller cancellation, lifecycle,
  retry-ordinal, result-replay, profile credential and publication selections.
- Full short client, controllerwire and capability suites.
- CLI burst, here attachment, profile publication and local cache selections.
- `TestBurstNativePreparationBuildsUploadedEditedSDK`, including real SDK build,
  tracked edited source upload, intake and source acquisition without sockets.
- Storage acceptance cells for remote-cache store edges, filesystem/S3 archives
  and filesystem/S3 publication components. Native matrix children stay skipped.
- Commentcheck against main, sleepcheck against HEAD, full doccheck and whitespace.

The three new/extended regression commands are:

```sh
go test -p 4 ./internal/nodehost -run '^TestOwnerHostWaitsForAlreadyRequestedRemoteCancellation$' -count=1
go test -p 4 ./internal/store -run '^TestOwnerCancellationProofKeepsIssuerAttemptAndFenceProperty$' -count=1
go test -p 4 ./internal/controller -run '^TestLocalHostPrivateTransportDrainsCancelledOwner$' -count=1
```

No archived test was newly ported and no native acceptance cell is closed.
The public CLI family and native HTTP SDK family compile and skip at this
sandbox's socket refusal. The authoritative native file still contains FAIL.
The lead must rerun its existing `^TestBurstPublicRun` and
`^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$` commands with
`-tags rewrite_integration`, without skips. The CLI family includes both profile
cache cases, both publication cases and personal cancellation.

Remaining real integrations are that native rerun, deployed Cloud/DinD and
network object transfers, and native platform/database lanes. Cache fixtures
supply verified App facts and do not prove live webhook verification. The
accepted requester-secret restart-loss residual is unchanged. Friction is the
sandbox's socket refusal and Overwing's reported source lag. No operational
state, installation, release or deployment changed. All checks are joined before
removing `/tmp/sw-astra-W9`; `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
