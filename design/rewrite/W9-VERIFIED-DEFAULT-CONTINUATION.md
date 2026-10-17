# W9 verified-default transport continuation

Base: `9e76a33b1`. No Git writes. Lead-owned handoffs and amendments are unchanged.

## Native burst failures

The supplied report predates the committed fixes. This session retained them and
reran their focused regressions rather than rebuilding them:

- Enrollment: the licensed fixture's real Session/CSRF client receives 201 from
  the public runner route. The unlicensed fixture caused the reported 401.
- Here: the execution-award and sealed-selection checks remain in place.
- Interrupt: the fixture passes its pipeline argument as `-- --block`, matching
  the documented CLI grammar. No public flag was added.
- Offline fallback: its assertion requires masked SDK output, restored cache and
  artifact markers, successful local history, and no restore/save warnings.
  Attached log streaming and local repository/ref namespaces remain fixed.

The public native CLI cases and all three SDK HTTP placement children still skip
because this sandbox refuses sockets. Their parent PASS lines prove no native
acceptance cell. The lead must rerun the commands below without skipped children.

## A1-50 implementation

The remaining remote-cache transport failed two new behavioral regressions:
private HTTP projection turned a valid verified-default memo into a silent miss,
and C1's generic record lookup did not select a ref when the caller omitted it.

C1 now selects among the live credential's authorized refs, using its existing
record lookup and edge checks. Remote-cache authority enforces verified-default
policy even without a caller flag. One `Record.VerifiedDefault` validator checks
persisted branch, App-delivery and SHA evidence for both C1 and C2. It also rejects
verified facts attached to a manual record key.

Successful verified-default selection carries one boolean observation through
the record result and HTTP response. The read-only cache consumes it instead of
requiring private source facts. Its exact logical selectors, manifest/member
validation, byte verification and permitted-artifact set remain enforced.

Invariant: no unverified cache selection grants a readable artifact, and no
selection observation replaces live authorization for a later download.
Generated store cases mutate persisted branch evidence; client fuzz cases refuse
missing attestations, stale miss payloads and changed refs; remote-cache fuzz
cases vary the local ref and attestation and refuse object signing without the
observation. The HTTP acceptance test reads verified bytes through the real
router/client and signed transfer interface, checks that private source proof is
absent, then revokes the credential and verifies the next artifact read fails.
Its storage, credential boundary and object transport are fakes; C1's selection
and provenance properties use real SQLite.

## Contract changes made

- C1: `RecordLookupResult.VerifiedDefault`, shared persisted-evidence validation,
  omitted-ref selection, and mandatory remote-cache verified-default policy.
- C3: optional hit-only `verified_default` on `DataRecordLookupResult`. It conveys
  an observation, never replayable authority or source evidence. Misses omit it.

No new route, grant, schema, credential, flag, setting, environment variable,
timeout, service, package, interface or retry mechanism. The smaller alternative
of exporting full RefFacts was rejected because it exposes private source facts.
Undo is reverting the observation and its consumers together.

Deleted 20 production lines and added 42, net +22 across all areas. This removes
the controller's duplicate validator and the cache's raw private-facts dependency;
no legacy package was newly retired. W9-owned production is +7/-6 this session,
net +1. Against main W9 is +18,513/-23,824, net **-5,311**, within the -2,900 budget.
No archived test was newly ported and no native matrix cell was newly unskipped.

Shared implementation files: `internal/blob/types.go`, `internal/blob/remote.go`,
`pkg/store/blob_lookup.go`, `pkg/controller/data_record_binding.go`, and
`pkg/controller/data_record_routes.go`. Shared tests:
`internal/blob/remote_test.go`, `internal/blob/remote_selector_test.go`,
`pkg/store/blob_remote_lookup_test.go`, `pkg/store/blob_default_lookup_test.go`,
and `tests/acceptance/data_cache_http_test.go`. The existing manual-scope fixture
now refuses inconsistent verified facts; real verified-scope success is covered
by the new SQLite test. C1/C3 docs, OpenAPI, the data guide, changelog and generated
mirrors were updated. Remaining code and client tests are W9-owned. The deleted
controller-private validator has no callers.

## Verification

Commands used `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`TMPDIR=/tmp/sw-astra-W9`, `GOTMPDIR=/tmp/sw-astra-W9/go`, and at most four package
or fuzz workers.

Passed: whole-repository build; focused vet on CLI/controller/client/store/blob/
controllerwire; complete short client/blob/controllerwire/clisurface suites;
selected controller native-data and SQLite record/cache suites; surface, here,
credential-policy and private-wire acceptance selections; CLI burst, enrollment,
local-host and secret properties; nodehost and localcontroller cache/storage
selections; focused race checks; commentcheck against main; sleepcheck against
HEAD; full doccheck; API regeneration; mirror and whitespace checks.

Fuzz campaigns passed: 12,913 client-attestation executions and 73,398 remote
selection executions, three-second budgets, four workers each. Native tests
were attempted and skipped at the socket boundary. The retry regression below
was rerun and remains red. One dependent-test invocation used a nonexistent
`internal/localcontroller` path; the corrected
`internal/wingd/localcontroller` selection passed.

## Retry-selection DESIGN explicitly handed back

`TestReviewRetryModeIsPartOfCommittedReplay` remains red: changing `failed` to
`all` under the same key replays the first execution. `EngineController.Retry`
validates Mode and drops it before CreateRun. Recommend one C1 admission-owned
selector that commits normalized mode and selected source-result fences in the
new execution lineage, includes canonical source ID plus mode in replay identity,
and supplies that committed selection to W3 after restart. C2 transports mode
separately from immutable request/spec seals. Require changed-mode conflicts,
numeric/ID alias replay and failed/all execution proofs. This is handed back to
the lead for C1/C2/W3 assignment and remains a product bug.

## Remaining work and integration

A1-50 remains incomplete for ordinary profile credential construction and local
publication. C2's repo-reader match requires `DataRemoteCache`, while delegation
cannot add a Data scope to an ordinary parent without one. The new observation
closes the private response mismatch; it does not invent a reader credential.
Recommend a server-owned repo-reader construction under existing account repo
authority and `data.read`, with the same verified-default and no-write policy.
Local publication still needs non-executing intake, authorized imported results
and logs, and failure reporting that preserves the local outcome. The CLI retains
its pre-intake refusals until those bindings exist.

Completed amendment and handoff work recorded in W9-MEMO-CONTINUATION.md remains
unchanged: A1-09/16/17/38/41/43/49/51/52, here-claim transport, source attempts,
writers/windows, aliases, identity, launcher grammar, doctor cleanup, local
composition, owner/eval separation, logs, service handover and decisions
110/111/113/114. A1-53 native evidence remains with the lead/T1. W1b materialization
is outside this change. Deployed Cloud runner/DinD/network/object integration,
native Darwin/Windows/PostgreSQL and signed fresh-machine verification remain
open. Requester-secret restart loss and one-replica topology retain their
documented residuals. Friction: socket refusal and Overwing's reported source lag.

## Exact native reruns

Require no skipped children:

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

All checks were joined. Scratch was removed and the shared build cache retained.
Git config has no local user override or core.worktree override. No installation,
Sparkwing pipeline, deployment, release or operational state change ran.

STATUS: CONTINUE
