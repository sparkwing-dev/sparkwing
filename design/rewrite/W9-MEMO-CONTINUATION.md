# W9 memo transport continuation

Base: `d25aa66da`. The supplied native burst report predates `3c97acfb1`.
That commit already fixes enrollment fixture licensing, here execution-award
validation, the interrupt fixture's `-- --block` grammar,
attached local log streaming and local dependency-cache namespaces. Those fixes
were retained and checked, not rebuilt. The real session route verifies the
CLI's existing Session authorization and CSRF presentation. No public `--block` flag was added.
The native fallback assertion requires masked SDK output, restored dependency
cache and artifact markers, and no cache restore/save warnings.

## Implemented

A1-50's native memo client now calls the existing authenticated lookup/copy
routes instead of returning `policy_missing`. One method owns both operations,
uses the shared request budget and sends only pipeline, job and hash selectors.
Copy destination comes from server authority. The client checks its bound scope,
the returned reader fence, required references, digests and member metadata.

Invariant: a memo result belongs to the current authenticated reader, and no
caller-supplied destination or read set becomes authority. The controller checks
C1's reader against its live binding. Its response includes that reader alone,
without private ancestry edges, uploader identity or source evidence. The client
leaves `ReadSet.Edges` empty. Downloads still require current server authorization.
No fake source proof was added to satisfy the remote-cache interface.

The real router/SQLite test commits output, manifest and artifact records,
looks up the memo, repeats the atomic copy, then uses `blob.MaterializeMemo`
through signed filesystem downloads to verify the actual bytes and manifest.
Generated client cases refuse changed team/run/job/generation/attempt, missing
reader, stale miss payload, missing references and invalid digest. The server
property refuses a foreign stored reader without exposing it. The existing
private-facts property now includes secret source edges and retains its refusal
to serialize them. Native binding properties cover both memo operations.

Deleted 16 production lines: the two memo refusal stubs, their shared refusal
helper, the controller-local memo response type and duplicated dependency-cache
digest validation. Added 82 production lines, net +66 across all areas. W9-owned
production is +75/-8, net +67. Against main, W9 is +18,512/-23,824, net -5,312,
within the -2,900 budget. No new package, interface, service, retry loop, flag,
environment variable, limit or timeout was introduced. No old API caller remains
for the deleted private response type or refusal helper.

## Contract changes made

C3 adds shared `DataMemoLookupRequest`/`DataMemoLookupResult` and a hit-only
`reader` response field to the existing lookup/copy routes. The server checks
that fence before returning it. Misses omit it. The contract documents that
this observation carries no ancestry proof or authority. No route or grant
changes. C1/C2/W3 implementations and the materialization state-machine path
remain unchanged.

Shared files touched: `internal/controller/data_cache_routes.go` and the new
`internal/controller/data_memo_client_test.go`; `tests/acceptance/data_cache_http_test.go`;
C3; `internal/apispec/wire.go`, generated `api/openapi.yaml`,
`docs/data-uploads.md` and its generated mirror; CHANGELOG.md and its generated
mirror. Other implementation and client tests
are W9-owned. No lead-owned handoff or amendment was edited.

## Verification

Commands used `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`TMPDIR=/tmp/sw-astra-W9`, `GOTMPDIR=/tmp/sw-astra-W9/go` and `go test -p 4`.

Passed:

- Whole-repository `go build ./...`.
- Complete client, short controllerwire/clisurface and API schema generator suites.
- Focused CLI burst, local-run, enrollment, here-award and attached-log regressions.
- Controller native-data routes and real-store memo lookup/copy/materialization.
- Local dependency-cache round trips and scope property.
- Cache HTTP scope/privacy/copy acceptance tests and foreign-reader property.
- W9 surface, here, credential-policy and private-wire amendment selections.
- Vet of CLI, controller, client and controllerwire.
- Reader-fence fuzz campaign: 13,847 executions in 3 seconds with four workers.
- Race checks for memo client validation and real-store materialization.
- Commentcheck against main, sleepcheck against HEAD and doccheck.
- Embedded-doc mirror check and whitespace check.

No acceptance cell was newly unskipped, and no archived test was newly ported.
The native public CLI selection and HTTP SDK placements were attempted. Their
socket-dependent children skip on `operation not permitted`; parent PASS lines
are not native evidence. No Sparkwing pipeline, candidate installation, Git
write, deployment, release or operational state change ran.

The lead should rerun exactly, checking that no selected child skips:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  TMPDIR=/var/tmp/sw-lead-tmp GOTMPDIR=/var/tmp/sw-lead-tmp \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  TMPDIR=/var/tmp/sw-lead-tmp GOTMPDIR=/var/tmp/sw-lead-tmp \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./internal/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' \
  -count=1 -v -timeout=5m
```

## Retry-selection DESIGN explicitly handed back

The real client/router/SQLite regression remains red: changing `failed` to `all`
under the same key returns the original execution with `replayed=true`.
`EngineController.Retry` validates Mode, then drops it before CreateRun. Recommend
one C1 admission-owned selector that commits normalized mode and selected source
result fences in execution lineage. Include canonical source ID plus mode in
replay identity, and give W3 the committed selection after restart. C2 transports
mode separately from immutable request/spec seals. Require changed-mode conflicts,
numeric/ID alias replay and failed/all execution proofs. This remains a product
bug handed back to the lead for C1/C2/W3 assignment, not a completed retry feature.

## Remaining assignments and real integrations

A1-50 remains incomplete for cross-profile cache and publication. The fake CLI
binding tests retain sealed read-only opt-in and pre-intake refusal. Public record
responses omit the private source evidence required by `blob.OpenRemoteCache`.
There is also no repo-reader construction/exchange for an ordinary profile
credential: C2 requires DataRemoteCache, while delegation refuses adding Data
to a parent without it. A remote-cache construction contract must reconcile
that requirement with the
existing HTTP privacy property. Recommendation: return a minimal server-attested
verified-default selection, never raw delivery keys, private source identities
or a caller-replayable read set. Do not synthesize `RefFacts` in the client.
Publication still needs non-executing intake, authorized imported results/logs
and failure reporting that preserves the local outcome. No publisher was faked
as a completed product feature.

The audit retains completed A1-09, A1-16, A1-17, A1-38, A1-41, A1-43, A1-49,
A1-51, A1-52 and named here-claim transport. The W9 handoff entries for source
attempts, step writers/windows, alias resolution, execution identity, launcher
grammar, doctor cleanup, local composition, owner/eval separation, shared logs,
service handover and decisions 110/111/113/114 remain implemented. This session
adds no work to those completed items. W1b's state-machine materialization
assignment remains with W1b and was not changed here.

A1-53 native placement evidence, deployed Cloud runner/DinD/network/object
integration, native Windows/Darwin/PostgreSQL and signed fresh-machine release
verification remain integration lanes. Requester-secret restart loss and the
one-replica topology retain their documented residuals. Friction: socket refusal
and Overwing's reported source lag. All checks were joined. Scratch was removed, and the shared build cache was
retained. Git identity remains the global default, with no core.worktree override.

STATUS: CONTINUE
