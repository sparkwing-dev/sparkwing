# W2 credential maintenance continuation

<!-- design:begin -->

## Implemented

Continues W2-mutations-handoff.md from 807684ed2. Two more routes use
PolicyRouter and the existing member/local team.admin policy:

- POST /api/v1/secrets/rotate.
- GET /api/v1/team/git-credentials/releases.

Both return no-store and canonical errors. Session rotation requires CSRF.
The authenticated team selects the adapter. Query parameters and headers
supply no tenant or actor authority. Rotation takes no body.

Invariant: a request can rotate or inspect credential rows only within its
authenticated tenant, and private values never enter maintenance responses.
Two generated HTTP tests vary tenant selectors, values, pipeline names,
masking and sharing. They use the real verifier, real bound cipher and fake
tenant interfaces. The fake rotation prepares replacement rows before its
commit so a callback failure leaves all original rows intact. It does not
prove a production store transaction.

CredentialRotator's transaction owns current authority checks and one team's
secret/git selection. It calls both transformation callbacks serially once
per row and commits both value-column rewrites together. Other fields stay
unchanged. The controller opens the complete row binding with current or
previous keys, then seals under the current key. Unreadable secrets keep their
bytes and appear by name/pipeline in skipped. Plaintext and unbound envelopes
never gain trusted binding. An unreadable git credential, any sealing failure
or key-location refusal aborts both tables. Provider diagnostics stay private,
including provider errors that implement apierr.Error.

SourceAuditReader returns final capability.SourceReleaseAudit records. The
public projection includes host, repository, run_id, job_id, generation,
runner, capability_prefix and released_at in Unix seconds. It excludes audit
IDs, credential IDs, owner account IDs and team fields. Foreign-team rows
fail the whole response. Empty lists remain arrays. The existing 200-row
bound stays.

The production tenant implements neither new interface in this snapshot.
Defaults refuse with policy_missing instead of invoking the old global
rotation or legacy audit reader. Missing resolver results are also refused
when a real SQLite store is present. Native session/peer transport and C1
production adapters remain separate integrations.

## Local contracts and W1 integration

These additions belong to C2. No C1 contract or store implementation changed.

- CredentialRotator exposes Team() Team and RotateCredentials(ctx, Authorized,
  func(Secret) (string, error), func(GitCredential) (string, error))
  (secretCount, gitCount int, err error). W1 must recheck current ancestry,
  team-admin membership or verified local authority and exact tenant in one
  transaction. Counts cover visited rows, including unchanged skipped secrets.
  Any callback or authority error rolls back both tables.
- SourceAuditReader exposes Team() Team and SourceReleaseAudits(ctx, limit)
  ([]capability.SourceReleaseAudit, error). W1 must read newest first using
  capability_prefix and nanosecond released_at. The controller converts the
  resulting time.Time to Unix seconds. This supersedes the earlier request
  to adapt GitCredentialReleases, whose legacy query uses token_prefix and
  seconds. W2 adds no SQL copy.

WithCredentialRotator and WithSourceAuditReader supply these interfaces to
embedded controllers and tests. The existing global Store rotation methods
and old audit projection are not valid implementations. Missing, nil and
foreign-team adapters fail closed.

Mechanisms added: the two tenant adapters above. The smaller alternatives
were rejected because existing global rotation has no tenant/authority input
and the legacy audit reader has the wrong schema projection. Removing these
adapters would require moving equivalent transaction/read contracts into C1
and updating the controller setters and callers together. No setting,
environment variable, compatibility alias or service was added.

## Deleted code and test ports

The production diff deletes 79 Go lines, including replaced lines and
formatting. It removes both old handlers, their legacy registrations and
tenant-route entries, global two-stage rotation, handler authorization,
anonymous actor fallback and token_prefix audit projection. No foreign
production caller required migration.

Deleted secrets_rotation_test.go and its 332 lines. Its six tests are covered
at the final HTTP boundary:

| Old test | Replacement |
| --- | --- |
| TestSecrets_PreviousKeyOpensValuesSealedUnderIt | TestCredentialRotationHTTPPreservesTenantAndRowBindings |
| TestSecrets_RotateReencryptsUnderTheCurrentKey | Same generated test, including both credential kinds and complete row bindings. |
| TestSecrets_RotateSealsValuesHeldAsPlaintext | Reversed in TestCredentialRotationHTTPSkipsUnreadableSecrets. Plaintext stays unreadable. |
| TestSecrets_RotateRefusedWithoutAKey | TestCredentialRotationHTTPBodyCipherAndCSRF, now unavailable rather than bad_request. |
| TestSecrets_RotateSkipsARowThatOpensUnderNoKey | TestCredentialRotationHTTPSkipsUnreadableSecrets covers a stray key, foreign binding, malformed envelope and plaintext. |
| TestSecrets_RotateRefusedWithoutTheAdminScope | TestCredentialMaintenanceHTTPRequiresLiveAuthority and member transport tests use the final team.admin rule. |

Eleven new maintenance HTTP tests pass. Two are generated property tests.
Callback failures, transaction refusals, missing dependencies, CSRF, member
transports, private provider diagnostics and foreign audit rows are covered.
The real SQLite missing-adapter test verifies no fallback mutates either
tenant. No new parked T1 function or native acceptance cell is marked complete.
Legacy combined git-release tests and startup reseal code remain until their
separate cutovers.

## Evidence

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W2. Tests use -p 4.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed. |
| Scoped vet on capability, apierr, controller, internal/acceptance, tests/acceptance and apiroutes | Passed. |
| capability and apierr suites | Passed. |
| Eleven maintenance HTTP tests | Passed. |
| Inherited W2 acceptance selection below | Passed. |
| PolicyRouter, authorized-handler, route-guard, tenant-route, issuance, SSH, source and plan-secret controller selection | Passed. |
| Orchestrator route-classification checks | Passed. |
| apiroutes, apispec and apiref suites | Passed after final specification generation. |
| commentcheck -base main . | Passed. |
| API specification drift and embedded docs mirror checks | Passed. |
| Controller API snapshot | Regenerated for both adapters and the final audit response. |
| Whole API snapshot | Inherited differences outside controller are untouched. |

The first maintenance test failed against both legacy routes' error envelopes
and authorization. It passed after migration. Initial API documentation
generation refused scope names in prose. The scopes now live in the table's
x-sparkwing-scope field, and generation plus drift checks pass.

Reproduce the focused checks with the environment above:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./internal/acceptance ./tests/acceptance ./internal/apiroutes
go test -p 4 ./internal/capability ./internal/apierr ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./tests/acceptance -run '^TestCredential(Maintenance|Rotation|Audit)HTTP' -count=1
go test -p 4 ./tests/acceptance -run '^Test(Credential|Authority|Data|Engine|Operator|Deployment|Source|Payment|Credit|Sizing|Discovery|Identity|Queue|FinalCredential|Reporting)' -count=1
go test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestRouteGuard_|^TestTenantRoutes_|^TestCredential(Issuance|SSHWrite)|^TestSourceController|^TestPlanSecretOpener' -count=1
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$' -count=1
go run ./internal/commentcheck -base main .
bash bin/check-api-spec.sh
bash bin/sync-docs.sh --check
```

No native HTTP listener, api.sock, PostgreSQL, placement or production adapter
coverage is claimed. The full controller suite, full acceptance suite and
known cancellation-intent failure were not rerun. Performance is unmeasured.
The manifest names the missing authenticated audit and rotation fixtures.

## Remaining work and amendment dispositions

Next W2 work is secret-by-name member/claim reads, remaining credential
metadata/rotation and trusted root provisioning, followed by the other legacy
route cutovers. Remove startup plaintext/pre-team reseal compatibility and its
callers when that cut is made. Native adapters and placement evidence stay
open. The area is incomplete.

| Amendment rows | Disposition |
| --- | --- |
| A1-01/03/05 | Finished W2 policy/error declarations retained. Real host integrations remain with W3. |
| A1-08/09/19/26/55 | Claim HTTP/selection retained. Enrollment, origin policy and execution-identity integrations remain open. |
| A1-11/16/17 | Source and private execution transport retained. Real host consumers remain open. |
| A1-14/15 | Failure and cancellation transport retained. W1 cancellation-intent persistence remains open. |
| A1-20 | Member and Actions issuance retained. Other root origins and credential administration remain open. |
| A1-21/22/23 | Two maintenance routes migrated in addition to the five prior writes. W1 transactions, session/console/peer adapters and remaining route cutovers remain open. |
| A1-24/25 | Stored source and plan-secret integration retained. Audit HTTP projection is done against the final read contract. W1's audit query and App-release audit remain open. |
| A1-27/28 | Billing calculation and signed checkout settlement retained. Other financial event integrations remain open. |
| A1-29/30/31/32/33/34/35 | Bound data/cache transport retained. W1 selection and projection, W7 physical storage and W9 client transports remain open. |
| A1-38 | Fake-backed launcher sync retained. Production transactional snapshot remains open. |
| A1-39/40/49/53 | Native consumers, runtime lifecycle and topology evidence remain with their owners and lead. |

The handoff entries addressed here are W1's source-audit projection and tenant
rotation requests. Their W2 HTTP pieces are done. The new precise W1 adapter
signatures are recorded in handoffs.md. Other entries have no new W2 request
beyond the remaining integrations above. No new architecture question arose.

## Shared files and friction

Shared changes: tests/acceptance/credential_maintenance_http_test.go,
design/rewrite/contracts/c2-credentials.md and handoffs.md, api/openapi.yaml,
docs/api-reference.md, docs/git-credentials.md, docs/migrations/_unreleased.md,
CHANGELOG.md and their generated mirrors, .apidiff/pkg_controller.txt and
perf/budgets.json. All production changes are in W2-owned controller code.
No foreign production file was changed.

Friction: Xwing current-context lookup could not write its session lock in
this sandbox. Work continued in the explicitly selected worktree. C1's
maintenance adapters and the inherited integrations above remain unprovided.
No Sparkwing binary, pipeline, release or Git write ran. The lead owns
independent review, commits and gates. Scratch is removed after final checks.

<!-- design:end -->
