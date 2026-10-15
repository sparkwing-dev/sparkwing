# W2 Actions exchange continuation

<!-- design:begin -->

## Implemented

The worktree started clean at 966e054b0. This continues the member issuance and
source/data work recorded in W2-issuance-handoff.md. No merge was pending.

`POST /api/v1/runners/github/exchange` uses PolicyRouter and AuthGitHubActions.
The external verifier checks the signed JWT and the persisted team's repository
and owner binding before the handler runs. All failures use the canonical error
object. Provider diagnostics and presented tokens stay out of errors and logs.
The request accepts only id_token and team. Other headers and query selectors
cannot supply authority. Successful responses use no-store.

The handler passes a trusted ProvisionActions to the existing CredentialIssuer,
which defaults to Store.ProvisionCapability and its sole Store.Issue transaction.
It sets fixed nodes.claim/triggers.claim grants, canonical gh:<id> repository
identity and the verified ref/SHA/workflow-run push binding. It sets no pools,
metering, data grants or App branch trust. The existing one-hour lifetime and
runner response fields remain. A failed issuer never returns its bearer.

Invariant: an issued Actions root gets its team, repository, push binding and
grants only from verified OIDC facts and the team's persisted repository binding.
Property tests generate body authority fields and signed cross-team/repository/
owner claims. They refuse every mismatch before issuance. The signed-token
tests use real RSA/JWKS verification with an in-process HTTP transport.

The SQLite HTTP test uses the real binding reader, issuer, capability storage
and default authenticator with a fake ProvisionPolicy. Missing policy refuses
issuance. A successful issue survives a digest lookup, permits identity discovery
and refuses sizing access as a root. A transaction-time policy refusal creates
no second capability. This is not a binding-removal race proof.

## Deleted and test ports

Deleted the 100-line old exchange mint block and 98 lines from its two ported
tests, 198 lines total. Removed its direct registration and guard expectation. The old Actions
fence and binding administration still serve legacy surfaces and remain for
their final cutover. No new compatibility path was added.

Route-inventory checks exposed three stale legacy entries left by earlier W2
cutovers: token creation, source release and machine consent. Those entries are
removed from tenant_routes.go. The direct-router guard also loses its three
stale data registrations and their two comment lines. The final handlers remain
registered through PolicyRouter and the source-derived API inventory.

TestGitHubRunnerExchangeNeedsBothSidesOfConsent is ported to the new HTTP
boundary: issuer/audience/signature/expiry refusal, two-sided team binding,
repository identity, transfer refusal, response fields and persisted lifetime
and workflow identity. The removed principal/token table assertions use the
new capability subject, digest lookup and push binding. Unpublished-key refusal
uses a valid JWT signed by another RSA key, separately from malformed signature
coverage. TestGitHubRunnerExchangeRefusesAnythingButABranchPush is ported for PR
and PR-target events with merge and branch refs, tags, missing SHA, and permitted
workflow_dispatch. Both old test functions are deleted. New coverage includes schedule, workflow_run, malformed
bodies, unknown authority fields, missing workflow identity, unavailable keys,
private errors, exact methods, missing dependencies and committed issuance.
The other tests in the old combined file remain because their claim scans,
binding removal, administration and enrollment findings are still open.

## Acceptance and verification

Six Actions HTTP component tests in tests/acceptance pass, including two
property checks and real-store issuance. The complete executable capability,
API-error, acceptance, route-parser and API-reference suites pass. Native T1
credentials cells remain skipped and no native placement is claimed complete.

Whole-repository build, scoped vet, policy-router/handler-policy checks,
credential issuance, encrypted plan-secret acceptance, source audit integration,
legacy route inventories and orchestrator route classification pass.
The initial API-spec drift failure was corrected by regeneration. The initial
legacy inventory failures were corrected by deleting the stale entries above.
The regenerated API/spec and controller API snapshot match their generators.
Documentation mirrors match. commentcheck -base main is clean across 1,014 files.
git diff --check passes.

The performance manifest records the external-proof/write exemption. Authorized
latency, cold start and response bytes were not measured in the sandbox.

Go used GOWORK=off, GOFLAGS=-buildvcs=false and GOMAXPROCS=4. GOCACHE, GOTMPDIR
and TMPDIR stayed under /tmp/sw-astra-W2. Package tests used -p 4. The scratch
directory is removed after the final checks. No Sparkwing binary, listener,
pipeline or git write ran. The lead owns commits, independent review and gates.

The full controller suite was not run because it includes native listeners and
retained legacy fixtures. The prior cancellation-intent failure, legacy source
fixtures and unrelated W9 SDK documentation drift are not claimed fixed.

Reproduce with the paths and Go environment above:

```sh
go -C /home/bombuntu/.xwing/envs/1080/sparkwing build -p 4 ./...
go -C /home/bombuntu/.xwing/envs/1080/sparkwing vet -p 4 ./internal/capability ./internal/apierr ./pkg/controller ./tests/acceptance ./internal/apiroutes
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./internal/capability ./internal/apierr ./internal/acceptance ./tests/acceptance ./internal/apiroutes ./internal/apispec ./internal/apiref
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./pkg/controller -run '^TestPolicyRouter|^TestAuthorizedHandlersDoNotAuthorize$|^TestRouteGuard_|^TestTenantRoutes_|^TestCredentialIssuance|^TestSourceController|^TestPlanSecretOpener|^TestGitHubRunnerWorkflowDocMatchesTheRenderedTemplate$' -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing test -p 4 ./internal/orchestrator -run '^TestEveryControllerRouteIsClassified$|^TestReadRouteListCarriesNoDuplicates$' -count=1
go -C /home/bombuntu/.xwing/envs/1080/sparkwing run ./internal/commentcheck -base main .
env -C /home/bombuntu/.xwing/envs/1080/sparkwing bash /home/bombuntu/.xwing/envs/1080/sparkwing/bin/check-api-spec.sh
bash /home/bombuntu/.xwing/envs/1080/sparkwing/bin/sync-docs.sh --check
```

## Contract changes and remaining integration

Local C2 additions are WithGitHubRunnerBindingLookup and the trusted Actions
registration selector convention. RegistrationID is
github:<repository-id>:<owner-id>, and the machine subject appends the workflow
run ID. No C1 or other area's signature changed. The exchange retry policy is
now never because the new bearer has no secret replay contract. C2 and the T1
route appendix record the correction.

C1 still needs its existing ProvisionPolicy implementation to recheck that
registration under the issuance lock, preserve applicable issuance limits and
revoke capability roots when a binding is removed. C1/W8 must finish real Actions
enrollment/profile resolution and the unified runner path. The fake origin
policy proves HTTP/store composition only. Missing production policy stays a
refusal. No second registry or SQL authorization owner was introduced.

The inherited contract requests remain: C1's session presentation digest and
CSRF persistence, separate cancellation-intent reason, audited App/extra-repo
release permit, credential metadata/rotation, launcher sync, live data metadata
and download charging, and authority-bearing operator/sizing writes.

W2 still needs browser/console/peer adapters, bootstrap/machine/service origins,
token administration, the remaining source/secret/log/account/queue cutovers,
specialized memo/depcache transport and final legacy auth deletion. W3/W8/W9
retain host, credential-file, enrollment and consumer integration. Native
HTTP/api.sock, Postgres, Windows/Darwin, real Actions and physical storage
evidence remain with their owners and T1/lead. No product-policy question was
introduced by this session.

## Amendment dispositions

No handoffs.md entry assigns W2 directly. Its W1 cancellation/session entry
still requires C1 implementation before W2 can consume the new facts.

| Rows | Disposition |
| --- | --- |
| A1-20 | Actions joins member provisioning through the sole issuer. Bootstrap, machine, service and token administration remain. |
| A1-21/22/23 | Existing snapshot/evidence policy remains. Actions now has an actual proof-bound PolicyRouter registration. Trusted browser/console/peer adapters and remaining route cutovers stay open. |
| A1-01/03/05 | Inherited decoder/service policy work remains complete at W2's boundary. W3 owns the production node-host router. |
| A1-08/09/19/26/55 | Mixed/named claims, here ancestry/windows and enrollment/version boundaries remain. Concrete enrollment and execution identity are still integration work. |
| A1-11/16/17 | Sealed source and private execution projections remain. Consumer integration stays with its owners. |
| A1-14/15 | Failure reads and holder-preserving cancellation remain. C1's separate cancellation intent is still needed. |
| A1-24/25 | Audited stored-source release and real encrypted-plan validation remain. App release permits and trusted production intake remain open. |
| A1-27/28 | Immutable billing provenance and signed committed-checkout settlement remain. Other verified billing-event contracts and production payment setup remain open. |
| A1-29/30/31/32/33/34/35 | Generic authenticated data adapters remain. Persisted authority/read/charge projections, physical transfer and specialized memo/depcache transport remain open. |
| A1-38 | Launcher sync retains the fake-backed contract. C1's production snapshot is still needed. |
| A1-39/40/49/53 | Concrete startup, credential files and native topology evidence remain with W3/W8/W9/T1/lead. |

## Shared files and assumptions

Shared edits outside W2 are internal/apiroutes/apiroutes.go and its test,
tests/acceptance/actions_issuance_http_test.go, the C2/T1 contract documents,
docs/github-actions-runners.md, generated docs/api-reference.md,
api/openapi.yaml, .apidiff/pkg_controller.txt, CHANGELOG.md and their generated
mirrors. No production caller outside W2 needed migration because the exchange
path and runner response fields remain.

perf/budgets.json also names the exchange's unmeasured corpus and exemption.

Mechanism added: the binding lookup override preserves the existing
Tenant.GitHubRunnerBinding signature plus its tenant selector. It permits
embedded stores and fake-backed HTTP refusal/property tests. A concrete Store
alone cannot supply wrong-scope responses or isolated read failures. Undoing it
removes the setter, field and override branch while retaining the default read.
No new environment variable, cache, service or persistence model was added.

Friction is the incomplete production origin policy and enrollment, remaining
legacy authorization/fixtures, and native evidence unavailable in the sandbox.

<!-- design:end -->
