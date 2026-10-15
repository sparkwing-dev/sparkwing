# W2 cache transport continuation

<!-- design:begin -->

## Implemented

This continues W2-actions-handoff.md from a clean worktree at ea2307aa1.

Three PolicyRouter registrations expose the existing RecordStore contract:

| Route | Request | Authority |
| --- | --- | --- |
| POST /api/v1/data/memo/lookup | pipeline, job, hash, optional deadline | Existing data-read alternatives |
| POST /api/v1/data/depcache/lookup | name, key, optional deadline | Existing data-read alternatives |
| POST /api/v1/data/memo/copy | Memo selectors | data.read and data.write on one sensitive claim |

Repository, ref and authority come from the persisted DataBinding. Memo copy
derives its destination from the current attempt. Query selectors cannot supply
those facts. The flat decoder refuses unknown, repeated, null and non-string
fields, missing selectors, invalid deadlines and trailing JSON. Responses omit
authority, uploader, source identity and ReadSet. Misses discard stale hit fields.
Memo responses carry public output/manifest digests and member metadata.
Depcache responses retain the winning restore source and public record metadata.

Invariant: every cache request derives repository, ref, authority and copy
destination from authenticated persisted facts, and responses omit private
authority/read-set snapshots. Generated tests exercise caller authority fields
and private persisted facts. C1 owns cache fallback, corruption/budget misses,
verified-default-only reads and atomic copied edges through its existing methods.
No fallback or SQL implementation was copied into W2.

Bound CopyMemo also checks DataLookup before DataRecord. Its direct-interface
test reproduced a write-only AccessRule reaching CopyMemo before this change.
The invariant is that copying requires both read and write authority at the
HTTP and in-process boundaries. Both paths now refuse a missing read grant.

The three paths and flat wire objects are local C2 additions. No other area's
Go signature changed. Lookups use RetrySafe. Copy uses RetryExactBody.

## Evidence and limits

Six cache HTTP component tests and the direct-interface copy test pass. The
new HTTP tests first failed because all three routes were absent. The direct
copy test first failed because a write-only AccessRule reached the fake store.
Two property tests generate authority selectors and private response facts.
Other cases cover methods, malformed bodies, missing/revoked/cancelled
authority, missing grants, failed scope/edge reads, unavailable wiring,
transaction errors, session CSRF, read-only remote-cache authority and misses.

Whole-repository build, scoped vet, capability/API-error/acceptance suites,
route parser and API generators, focused controller policy/issuance/source tests,
API/spec drift, docs mirrors and commentcheck pass. The controller API snapshot
matches. The repository-wide API snapshot check fails on inherited match,
projectconfig, storage/storeurl, store and Go SDK changes. Those other areas' snapshots were not
rewritten. The route
classification check initially rejected the new paths. The existing data-route
classification inventory now includes them, and the check passes.

Native T1 cells remain unchanged. No native placement or physical transfer
completion is claimed. No old test was ported or legacy runtime deleted in this
slice: zero legacy lines deleted. This adds missing transport to already
declared interfaces. Previous W2 test ports and deletions remain in the earlier
handoffs.

The full controller suite is not green. Its existing
TestEngineCancelJobKeepsLiveHolderAndRechecksOwner still fails at the persisted
cancel-reason assertion. C1 has not supplied the separate cancellation intent.
The existing session digest fixture remains necessary: the store resolves a
session hash but omits Capability.Digest in its returned principal. Its default
verifier still refuses that principal. CSRF still uses legacy persistence.

## Real integration points and contract requests

W1 must complete logical-record selection under DataAccessChecker. Before a
hit is selected, DataLookup has RecordKind/RecordScope/RecordName and no Digest.
CopyMemo's DataRecord precheck also has no selected Digest. The merged
Tenant.CheckDataAccess treats lookup as a digest-presence query, and refuses it
with `invalid blob digest`. A SQLite overlay probe reproduced this with a live
remote-cache root, DataRead, repository gh:1, ReadRefs=[main] and
VerifiedDefaultOnly=true:

```go
err := tenant.CheckDataAccess(ctx, authorized, capability.DataTarget{
    Operation: capability.DataLookup, RecordKind: "memo", RecordName: "hash",
})
```

The root was persisted with engineHTTPRoot, its CapabilityRecord and repo_key
updated together, and reloaded through LookupCapability before this call.
The failed probe lived only in scratch and was removed. No production failure
was turned into a passing or skipped integration test. W1 owns resolving live
selection authority and retaining per-digest/member checks inside its record
transaction. W2 keeps using the declared interface and fakes until then.

W1 still owns the missing ResolveAuthority/live metadata/charging projections.
W7 still owns shared namespace/edge and physical object implementations.
W9's NewDataClient remains a panic stub and needs all three transports using
the C2 wire objects above. W3/W8/W9 own host, remote-cache and credential-file
consumers. Full HTTP/api.sock, Postgres and native placement evidence stays open.

The inherited requests for origin policy, Actions registration revocation,
source/App release, credential metadata/rotation, launcher sync, operator/sizing
transactions and non-checkout verified billing events remain open.

## Amendment and handoff dispositions

| Rows | Disposition |
| --- | --- |
| A1-01/03/05 | Existing W2 decoder and node-service policies retained. Production host routing remains W3's integration. |
| A1-08/09/19/26/55 | Existing mixed/named claims, here and version boundaries retained. Enrollment and execution identity integrations remain open. |
| A1-11/16/17 | Existing sealed-source and private execution projections retained. Host consumers remain open. |
| A1-14/15 | Existing failure reads and holder-preserving cancellation retained. Cancellation-intent persistence is still missing. |
| A1-20 | Member/Actions issuance retained. Bootstrap, machine, service, origin policy and token administration remain open. |
| A1-21/22/23 | Three actual cache PolicyRouter registrations added. Session digest/CSRF, console/peer adapters and other route cutovers remain open. |
| A1-24/25 | Existing source audit and encrypted-plan-secret checks retained. App release permits and production intake remain open. |
| A1-27/28 | Existing immutable billing and signed checkout settlement retained. Other verified event kinds and payment setup remain open. |
| A1-29/30/31/32/33/34/35 | Cache record transport added through the existing interfaces. W1 logical-selection authorization, W7 storage, W9 clients and native proofs remain open. |
| A1-38 | Existing fake-backed launcher sync retained. Production snapshot remains open. |
| A1-39/40/49/53 | Native startup, credential files and topology evidence remain with their owners and lead. |

handoffs.md's W1 cancellation/session request remains open. It now also names
the reproduced logical-selection mismatch. No W4 review handoff assigned a new
W2 implementation.

## Shared files and assumptions

Shared edits are internal/acceptance/credential_routes.go,
internal/orchestrator/wingd_api_routes_test.go, tests/acceptance/data_binding_test.go,
tests/acceptance/data_cache_http_test.go, C2/T1 contract prose, docs/data-uploads.md,
generated docs/api-reference.md and api/openapi.yaml, CHANGELOG.md, their generated
mirrors, and perf/budgets.json. No foreign production caller needed migration.

Mechanism added: a flat string-object cache decoder rejects duplicate/null
selectors. The existing engine decoder accepts both, so it cannot enforce the
local wire contract. Undoing it means changing that contract and using the
generic decoder. No interface, configuration, environment variable, persistence
model, service or cache was added.

Performance remains unmeasured. The manifest records the authorized-fixture
exemptions. Refusal latency is not evidence for a cache lookup or copied hit.

Go checks used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4 and
GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W2. No Sparkwing binary, listener,
pipeline, Git write or external-home mutation ran. Scratch is removed after
verification. The lead owns commits, independent review and gates.

Reproduce from the worktree with the environment above:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/acceptance
go test -p 4 ./internal/capability ./internal/apierr ./internal/acceptance ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestRouteGuard_|^TestTenantRoutes_|^TestCredentialIssuance|^TestSourceController|^TestPlanSecretOpener' -count=1
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$' -count=1
go run ./internal/commentcheck -base main .
bash bin/check-api-spec.sh
bash bin/check-api-snapshot.sh
bash bin/sync-docs.sh --check
```

<!-- design:end -->
