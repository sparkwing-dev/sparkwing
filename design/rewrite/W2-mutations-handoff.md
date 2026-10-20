# W2 credential mutation continuation

<!-- design:begin -->

## Implemented

Continues W2-inventory-handoff.md from 1ab5548a4. Five routes now use
PolicyRouter and the existing member/local team.admin alternatives:

- POST /api/v1/secrets.
- DELETE /api/v1/secrets/{name}.
- POST /api/v1/team/git-credentials.
- POST /api/v1/team/git-credentials/{host}/confirm.
- DELETE /api/v1/team/git-credentials/{host}.

The production default refuses these writes with policy_missing until W1
implements the authority-bearing writer. Existing Tenant writers take no
authority, so using them after an HTTP check would leave the committing
revocation/membership race open. This is an incomplete integration, not a claim
that production credential administration works.

Invariant: only live team-admin authority can commit a credential mutation
within its authenticated tenant, and envelopes bind to that tenant and the
complete row. Two generated tests vary secret values, caller tenant selectors,
pipeline names and masking/shared fields. They exercise the real verifier,
fake transaction interface and real SQLite persistence/encryption. The fake
records the exact Authorized input and can refuse a raced transaction. It does
not prove the production transaction recheck.

CredentialWriter and WithCredentialWriter declare that local C2 boundary.
Missing, nil and foreign-team writers fail closed. W1 owns current authority,
membership, tenant, clock and actor checks at commit. No W2 SQL, persistence
copy, new setting, environment variable or unfenced fallback is added.

The handlers derive creator identity from the verified subject and team from
Authorized. Session writes require CSRF. Every response uses no-store.
Cipher/provider/store diagnostics stay private. Typed bodies reject unknown
authority fields and trailing input. Masking defaults to true. Secret names
use the shared grammar even when replacing an existing row. SSH writes retain
host-key scanning and pinning, public-key output and confirmation. Omitted SSH
port means 22. Explicit zero is refused. Confirmation checks the pinned key.

The credential registrar uses method-specific registrations because by-name
secret reads, rotation and audit reads still have legacy registrations. A
path-only registrar would shadow those remaining routes.

## Deletion and test ports

The production diff removes 168 Go lines, including replaced lines and
formatting. It removes the five old scope registrations, handler authorization,
anonymous creator fallback, old boundCipher response helper, invalid-name
grandfathering helper and unused resGitCredential declaration.

Removed 84 old test lines. TestSecrets_NoCipherStoresNothing and
TestSecrets_KeyRefusalReachesTheClientAndOtherSealFailuresStayMasked are ported
to TestCredentialMutationsHTTPEncryptionFailuresStoreNothing, which also covers
git credentials. TestSecrets_ExistingRowKeepsANameTheRuleNowRejects is reversed
by TestCredentialMutationsHTTPValidatesBodiesBeforeTransactions: invalid names
remain invalid when a row already exists. Its old failingSealCipher fixture
and unused imports are removed. Other legacy secret read/rotation and combined
git-release tests remain until those routes are replaced.

No parked T1 function was ported in this slice. Nine mutation HTTP tests pass,
including two property tests. TestCredentialSSHWritePinsHostAndKeepsKeyPrivate
passes all five cases without a network listener. It covers default/explicit
port, zero refusal, malformed key and private scan failure. Native placement,
HTTP listener and api.sock acceptance cells remain open. None was unskipped or
reported complete.

## Contract changes needed

W1 must implement the five authority-bearing CredentialWriter methods declared
in internal/controller/credential_mutations.go, using Authorized and its own clock.
Existing Tenant methods cannot provide this guarantee. The C2 declaration and
handoffs.md list the signatures. No C1 contract file or implementation was
changed. W2 tests with fakes instead of bypassing the transaction boundary.

Team-scoped secret/git key rotation also needs an authority-bearing transaction.
The existing Store.RotateSecretValues and RotateGitCredentialSecrets walk every
team, while C2 declares a team-owned member route. Neither a controller-side
loop over unfenced writes nor a global rotation can satisfy that contract.
The legacy rotation remains a separate unfinished cutover.

Earlier W1 requests remain open: cancellation intent, session digest/CSRF,
logical-record selection authorization, source-release audit projection,
transactional origin policy, Actions-registration revocation, launcher snapshot,
operator/sizing write rechecks and additional verified payment-event contracts.

## Evidence

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and
GOCACHE/GOTMPDIR/TMPDIR beneath /tmp/sw-astra-W2. Tests use -p 4.

| Check | Result |
| --- | --- |
| go build -p 4 ./... | Passed after the route and writer changes. |
| Scoped vet: capability, apierr, controller, internal/acceptance, tests/acceptance and apiroutes | Passed. |
| capability and apierr suites | Passed. |
| Nine TestCredentialMutationsHTTP tests | Passed, including generated tenant/envelope checks. |
| W2 acceptance selection below | Passed. Includes inherited credential, engine, data, billing, source and operator checks. |
| PolicyRouter, authorized-handler, route-guard, tenant-route, issuance, SSH, source and plan-secret controller selection | Passed. |
| Orchestrator route-classification checks | Passed. |
| apiroutes, apispec and apiref suites | Passed on the generated final spec. |
| commentcheck -base main . | Passed. |
| check-api-spec.sh and docs mirror check | Passed. |
| Controller API snapshot | Regenerated for CredentialWriter and its setter. |
| Whole API snapshot | Still differs in inherited match, projectconfig, retired runner, storage/storeurl, store and SDK surfaces. Those snapshots are untouched. |
| Whole tests/acceptance suite | Failed in W5 native TypeScript tests because sdk/typescript/node_modules/typescript/bin/tsc is absent. No W5 files or dependency installs were changed. |

The first apispec suite ran before generation completed and reported the old
scope rows. After generation, the apispec suite and spec drift check pass. The
initial mutation tests failed against legacy routing, including a nil-store
panic before the shared authorization boundary. The migrated tests pass.

Reproduce the focused checks from the worktree with the environment above:

```sh
go build -p 4 ./...
go vet -p 4 ./internal/capability ./internal/apierr ./internal/controller ./internal/acceptance ./tests/acceptance ./internal/apiroutes
go test -p 4 ./internal/capability ./internal/apierr ./internal/apiroutes ./internal/apispec ./internal/apiref
go test -p 4 ./tests/acceptance -run '^Test(Credential|Authority|Data|Engine|Operator|Deployment|Source|Payment|Credit|Sizing|Discovery|Identity|Queue|FinalCredential|Reporting)' -count=1
go test -p 4 ./internal/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestRouteGuard_|^TestTenantRoutes_|^TestCredential(Issuance|SSHWrite)|^TestSourceController|^TestPlanSecretOpener' -count=1
go test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$' -count=1
go run ./internal/commentcheck -base main .
bash bin/check-api-spec.sh
bash bin/sync-docs.sh --check
```

Performance is unmeasured. The manifest names the missing authenticated write
fixtures and why a scheduled write probe would measure refusal or mutate real
credential authority. No Sparkwing binary, socket listener, pipeline, release
or Git write ran. The lead owns independent review, commits and gates.

## Amendment and handoff dispositions

| Rows | Disposition |
| --- | --- |
| A1-01/03/05 | Finished W2 policy/error declarations retained. Real host integrations remain with W3. |
| A1-08/09/19/26/55 | Claim selection and HTTP work retained. Enrollment, origin policy and execution-identity integrations remain open. |
| A1-11/16/17 | Source and private execution transport retained. Real host consumers remain open. |
| A1-14/15 | Failure and cancellation transport retained. W1 cancellation-intent persistence remains open. |
| A1-20 | Member and Actions issuance retained. Other root origins and credential administration remain open. |
| A1-21/22/23 | Five mutations migrated. New CredentialWriter transaction integration, session/console/peer adapters and remaining route cutovers remain open. |
| A1-24/25 | Stored-source release and plan-secret integration retained. App release and audit projection remain open. |
| A1-27/28 | Billing calculation and signed checkout settlement retained. Other financial event integrations remain open. |
| A1-29/30/31/32/33/34/35 | Bound data/cache transport retained. W1 selection and projection, W7 physical storage and W9 client transports remain open. |
| A1-38 | Fake-backed launcher sync retained. Production transactional snapshot remains open. |
| A1-39/40/49/53 | Native consumers, runtime lifecycle and topology evidence remain with their owners and lead. |

No new handoff assigned W2 work beyond these rows. Credential metadata/rotation,
remaining root provisioning and route migrations, native transport and Postgres
coverage are unfinished. W2 is incomplete.

## Shared files and friction

Shared files: internal/apiroutes/apiroutes.go and its test,
tests/acceptance/credential_mutations_http_test.go, api/openapi.yaml,
docs/api-reference.md, docs/auth.md, docs/git-credentials.md,
docs/migrations/_unreleased.md and their generated mirrors, CHANGELOG.md and
its mirror, .apidiff/pkg_controller.txt, perf/budgets.json and C2/handoffs prose.
All other production changes are in W2-owned internal/controller. No foreign
production caller needed migration. No C1 implementation was edited.

Friction: missing authority-bearing store APIs, absent TypeScript compiler,
inherited API snapshot drift and the first spec check racing regeneration.
The latter was corrected and rechecked. The former integration limits remain
explicit. The full controller suite, native placement acceptance, Postgres,
runtime performance and the known cancellation-intent failure are not claimed
as passing. The unchanged cancellation failure was not rerun.

<!-- design:end -->
