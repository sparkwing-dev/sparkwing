# W2 credential inventory continuation

<!-- design:begin -->

## Implemented

This continues W2-cache-handoff.md from 7e80b1d3d. GET /api/v1/secrets and
GET /api/v1/team/git-credentials now register through PolicyRouter. The existing
C2 rules select live member or verified local authority. Secret inventory
accepts runs.read or team.admin. Git inventory requires runs.read.

Invariant: inventory reads select the authenticated team, omit masked secret
values and stored git credentials, and fail when public config decryption fails.
Generated tests vary private values and caller tenant selectors. A second
property test varies secret values and changes team, name, pipeline, shared or
masked envelope binding. Every changed binding fails without returning rows.

The two handlers use CredentialInventoryReader, a local C2 subset of Tenant's
existing methods. Store.ForTeam is the default. Embedded controllers and tests
can supply the same reader. Nil and foreign-team readers fail closed. No SQL,
new persistence model or C1 signature is added. This seam supports the requested
fake-backed integration. Removing the exported seam would require moving those
tests onto store fixtures and removing embedded-controller injection.

Masked rows never require a decryption key. Public config uses its stored
binding. Missing decryption returns unavailable. A failed open returns internal
without the cipher diagnostic or any partial list. Git metadata uses the
existing public projection. Both responses use no-store and empty arrays.

The initial real-SQLite test failed because the old handler selected the default
tenant and returned an empty list. The migrated handlers return the authenticated
tenant's two secret rows and its git metadata despite foreign query/header hints.
The test uses fake authentication snapshots with the real verifier and real
Tenant reads. It does not prove native transport or default session persistence.

## Deletion and test ports

Removed 22 legacy production lines across the replaced handler checks, old
registrations and tenant-route inventory. Removed 14 old list assertions from
TestSecretsRoundTrip. Their name, count and masked-value assertions now run in
TestCredentialInventoryHTTPUsesAuthorizedTenant with current authority.
Other old secret CRUD tests remain because their routes still need migration.
These counts exclude whitespace-only Server field alignment.

No parked T1 test function was ported in this slice. Eight inventory HTTP tests
and one API route-discovery test pass. No native acceptance cell was unskipped.
The matrix scenario remains incomplete until every controller and node-host
registration and authority alternative is exercised.

## Evidence

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
-p 4 where supported, and GOCACHE/GOTMPDIR/TMPDIR under /tmp/sw-astra-W2.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed. |
| Scoped vet: capability, apierr, controller, acceptance and apiroutes | Passed, including the final inventory tests. |
| capability, apierr, internal/acceptance, tests/acceptance, apiroutes, apispec, apiref suites | Passed. |
| Eight TestCredentialInventoryHTTP tests | Passed on the final test file. Includes two property tests. |
| PolicyRouter, authorized-handler, route-guard, tenant-route, issuance, source and plan-secret controller checks | Passed. |
| Orchestrator route classification and duplicate-read-route checks | Passed. |
| commentcheck -base main . | Passed. |
| API spec and docs mirror checks | Passed. Public route paths and response types are unchanged. |
| Controller API snapshot | Matches the regenerated snapshot. |
| Whole API snapshot check | Fails on inherited match, projectconfig, storage/storeurl, store and SDK changes. Those snapshots are untouched. |
| TestEngineCancelJobKeepsLiveHolderAndRechecksOwner | Still fails on the missing cancellation reason. W1's existing handoff remains open. |

The final checks can be reproduced from the worktree with the environment above:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/acceptance ./internal/apiroutes
go test -p 4 ./internal/capability ./internal/apierr ./internal/acceptance ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestRouteGuard_|^TestTenantRoutes_|^TestCredentialIssuance|^TestSourceController|^TestPlanSecretOpener' -count=1
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$' -count=1
go run ./internal/commentcheck -base main .
bash bin/check-api-spec.sh
bash bin/check-api-snapshot.sh
bash bin/sync-docs.sh --check
```

No Sparkwing binary, listener, pipeline, Git write or release ran. No process was
left running. Scratch is deleted after verification. The lead owns independent
review, commits and gates. Performance is not measured. The budget manifest
records why an authorized inventory corpus is still required.

## Remaining contracts and integrations

W1's existing cancellation-intent, session digest/CSRF and logical-record
authorization requests remain open. Read inspection still shows the missing
session digest, sparkwing_meta CSRF storage and DataLookup's digest-presence
path. No duplicate W2 persistence or authorization fallback was added.

Credential metadata/list selection, authority-bearing rotation, transactional
origin policies, Actions-registration revocation, audited App release, launcher
snapshot, operator/sizing write rechecks and additional verified payment events
remain open under the earlier handoffs. Secret and git mutation routes still
use the old stack. Default console/peer adapters and remaining route cutovers
also remain open. W2 is incomplete.

Additional W1 request: source-release audit reads need the final projection.
Tenant.GitCredentialReleases still selects token_prefix and decodes seconds.
ReleaseSourceCredential writes capability_prefix and nanoseconds. This is a
code-inspection mismatch, not a new passing integration claim. W2 did not migrate
the audit list against that retired projection or change C1's contract.

W7 physical storage, W9 NewDataClient transports, W3/W8/W9 credential and host
consumers, native HTTP/api.sock/placements and Postgres remain open as recorded
in W2-cache-handoff.md. NewDataClient is still a panic stub.

## Amendment and handoff dispositions

| Rows | Disposition |
| --- | --- |
| A1-01/03/05 | Completed W2 policy/error work retained. W3's real host router remains open. |
| A1-08/09/19/26/55 | Completed claim policy/transport retained. Enrollment and execution-identity integrations remain open. |
| A1-11/16/17 | Sealed source and private execution projections retained. Host integrations remain open. |
| A1-14/15 | Failure reads and cancellation transport retained. W1 cancellation-intent persistence remains open. |
| A1-20 | Member and Actions issuance retained. Remaining root provisioning and token administration remain open. |
| A1-21/22/23 | Two inventory registrations migrated. Session digest/CSRF, console/peer adapters and other route migrations remain open. |
| A1-24/25 | Audited stored-source and plan-secret integration retained. App release and the final audit read projection remain open. |
| A1-27/28 | Existing billing and signed checkout settlement retained. Other financial integrations remain open. |
| A1-29/30/31/32/33/34/35 | Completed bound data and cache HTTP work retained. W1 selection/projection, W7 storage and W9 transports remain open. |
| A1-38 | Fake-backed launcher sync retained. Transactional production snapshot remains open. |
| A1-39/40/49/53 | Native consumers and topology evidence remain with their owners and lead. |

handoffs.md's W1 requests remain open. No newly assigned W4 handoff required W2
runtime changes. The new audit-reader request is added to handoffs.md.

## Shared files and friction

Shared files are internal/apiroutes/apiroutes.go and its test,
tests/acceptance/credential_inventory_http_test.go, C2/handoffs prose, auth/git
source docs and their generated mirrors, migration/changelog and mirrors,
.apidiff/pkg_controller.txt and perf/budgets.json. No foreign production caller
needed migration.

The legacy tenantRoutes inventory initially retained the moved reads and failed
its check. Both obsolete entries are removed. The first wrong-audience test used
a dead node-host fixture and got 401. A live process/binding fixture now establishes
403. These were corrected without changing authorization behavior.

Remaining test friction is the inherited full API snapshot drift and cancellation
failure above. The full controller suite, native acceptance, Postgres and runtime
performance were not claimed as passing.

<!-- design:end -->
