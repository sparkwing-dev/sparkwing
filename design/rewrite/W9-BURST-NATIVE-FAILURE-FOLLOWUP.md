# W9 burst cancellation and native observation

Base: `09d1af645`, with the integration merge already in progress on arrival.
No Git writes ran. All nine conflicting files have resolved contents. The index
still lists them as unmerged because the lead owns staging and committing.
Integration deletions stay deleted. C1 keeps the combined version-6 baseline,
including the incoming GitHub and schedule additions. No compatibility path was
added. C2 retains both operator-settings binding and source-bundle binding.

The authoritative native report still contains FAIL for detached profile-cache
publication and here/personal cancellation. This session cannot mark those
native cases passed. The lead must commit this work and rerun its harness.

The cancellation invariant is that a live owner can drain cancelled work without
restoring requester values or regaining secret-read authority. The real HTTP,
client and SQLite regression failed in 0.14 seconds when a cancelled owner's
heartbeat included local_secrets. Empty heartbeats worked. Availability and
finish were tested independently and worked. Resupply had applied the sensitive
secret-read cancellation fence before reporting renewal could return cancellation.

Resupply now locks the owner and run in the existing order, validates the sealed
names, and returns without installing values once cancellation is committed.
Active runs still use the complete sensitive fence. Stale and expired owners
remain refused. The generated real-store property verifies no values are
restored during drain. No new endpoint, setting, field, timeout, wrapper or
retry loop was added.

The detached cache test's publication latch also observed the Cloud cache-seed
execution's finish. It could read the later publication while its jobs were
still being imported. The latch is now armed after the seed finishes. A new
native assertion rejects premature notification. This is a test correction,
not a change to publication execution. The native lane must verify it.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9,
GOTMPDIR=/tmp/sw-astra-W9/go and at most four package workers.

- Whole-repository `go build -p 4 ./...` passes. Vet passes for store,
  controller, CLI, capability and API-route packages.
- Focused Burst, LocalSecrets, RequesterLocalSecret, Publication, RunRouting,
  HostileProject, ProjectUntracked, schedule discovery, engine bootstrap and
  API parser tests pass. The CLI package takes 34.1 seconds, controller 13.5
  seconds and store 5.6 seconds. Embedded docs mirror and privacy checks pass.
- `TestBurstCancelledOwnerRenewalRetainsReportingAuthority` and
  `FuzzCancelledHereResupplyNeverRestoresSecrets` pass with the race detector.
  The latter also passes 30 fixed-count fuzz executions.
- `FuzzHostileProjectCannotAuthorizeBurst` and
  `FuzzBurstCLIClaimantsStayInsidePreferenceSet` each pass four fixed-count
  executions through the existing real CLI preparation, HTTP and SQLite paths.
- Commentcheck against main, sleepcheck against HEAD and diff whitespace pass.
- The rewrite_integration CLI and SDK HTTP probes compile, then skip at the
  forbidden TCP listener. Those skips are not native acceptance evidence.

The cancellation reproduction is `go test -p 4 ./pkg/controller -run
'^TestBurstCancelledOwnerRenewalRetainsReportingAuthority$' -count=1` with the
same environment above. It passed after the fix. The lead-native rerun needs
`go test -p 4 -tags rewrite_integration ./cmd/sparkwing -run
'^TestBurstPublicRun' -count=1` and the controller's
`^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$` family.

The merge exposed API-doc test expectations that repeated identical grants.
They now follow the incoming parser's deduplication while retaining the distinct
source.read alternative on bundle downloads. All three failing parse tests ran
red before correction. No production parser or authorization was weakened.

Contract changes made: C1 and C3 document cancelled resupply as a no-op for the
live here owner. C1 reconciles the merged baseline note to version 6. The user
guide and embedded mirror retain the cancellation-drain guarantee and state
that secret reads remain refused. Contract changes needed: none. Lead-owned
amendments and handoffs are unchanged. Previously completed W9 assignments and
B1/S1-S5 implementations are retained. No archived test was newly ported and no
acceptance cell was newly unskipped.

Shared files touched beyond W9 ownership:

- pkg/store/local_secrets.go and local_secrets_test.go.
- pkg/controller/burst_cancellation_test.go.
- Merge resolutions in internal/capability/policy.go,
  internal/apiroutes/apiroutes_test.go, pkg/store/dialect.go, pkg/store/store.go,
  and pkg/controller/engine_amendment_test.go.
- C1/C2/C3/C4 contract notes and pkg/docs/mirror/local-execution.md.

The remaining merge resolutions are cmd/sparkwing/schedules_repository_environment_test.go
and the contract files listed above. Other W9 edits are the native burst test,
docs/local-execution.md and this report. No API was removed, so no production
caller migration was needed. Incoming integration deletions were preserved.

The production fix is +10/-1, net +9 lines. Whole legacy files deleted this
session: 0. The inherited burst addition grows by nine production lines.
W9-owned production versus main, including the integration merge, is
+19,127/-23,815, net -4,688, within the -2,900 area budget.

Remaining integration: the lead's socket-dependent native suite, especially
detached profile-cache publication and both cancellation placements. Native
controller restart/reconnect with remote workers, deployed Cloud/DinD,
Windows/Darwin and PostgreSQL remain the lead's wider acceptance lanes.
No foreign implementation stub blocks this fix. Pending product decisions in
the burst review are unchanged. Friction is the sandbox's socket refusal and
read-only Git metadata. No changelog entry is needed for this correction to the
existing rewrite contract. Scratch is removed after checks join. The shared
build cache is retained.

STATUS: CONTINUE
