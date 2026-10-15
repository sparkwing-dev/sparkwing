# W2 adversarial review fixes

The W2 HTTP fixes and test repairs are implemented. This change is not ready to
merge as a completed rewrite. Native route completeness tests remain red, and
several fixes require contracts owned by other areas.

| Finding | Status | Result |
| --- | --- | --- |
| 1, blocker | PARTIAL; W1 DESIGN handoff | Direct-session origin is centralized. Derived sessions delegate with complete ancestry and its shortest effective lifetime, and cannot pass `CredentialAccount`. A real SQLite login/mint/revoke test passes. Direct store root issuance and owner-consent commit gates remain W1 work. |
| 2, major | OPEN; DESIGN handoff | Native tests reproduce missing production adapters, local peer refusal and missing data authority. The reproduction patch and intake handoff name each concrete type and transaction. |
| 3, major | OPEN; DESIGN handoff | Both real schedule and verified-webhook intake reproduce `triggers has no column named pipeline`. Persisted origin authority and verified delivery facts must be declared before migrating and deleting legacy intake. |
| 4, major | PARTIAL | Real API, browser, metrics and node-host registrations are checked against the independent inventory in both listener configurations. Tests expose 23 missing handlers. The separate metrics listener's policy bypass is fixed. |
| 5, major | PARTIAL; DESIGN handoff | Dashboard callers use final run, job metrics, pause-read, log and resume paths. Cancel/retry send JSON. Trigger uses the final request envelope. Production canonical repository resolution and the missing resume handler remain open. |
| 6, major | OPEN; DESIGN handoff | A real awarded claim with a declared pipeline reference still fails the native checker. The reporting property now tests rejections through actual serving handlers and claims no successful reference integration. |
| 7, major | OPEN; DESIGN handoff | A real HTTP claim ignores configured local-first hold despite an eligible live preferred runner. The shared matcher needs a W1-owned placement policy input. Misleading controller comments are removed. |
| 8, minor | FIXED | Verified paid-card/warning events require a fetched fingerprint. Disputes require a reason. Generated malformed-event proofs fail before settlement. |
| 9, minor | FIXED | API parser follows final metric endpoint groups. Its actual discovery regression includes metrics and usage POST routes. |
| 10, minor | FIXED | Login budgeting no longer depends on Argon slots. The existing 30/client and default 2,400/listener per-minute limits remain. Obsolete hashing telemetry and its source/mirror documentation are deleted. |
| Additional stale comments | FIXED | Removed dispatcher, future-policy, filesystem-log and cron dispatch/history claims. Cron queue age uses final-schema CreatedAt. Queue-timeout and placement comments no longer promise unimplemented behavior. |

## Authority invariant and structural changes

Changing transport preserves authority ceilings, ancestry and lifetime, and
cannot establish owner consent. `Principal.DirectAccountSession` derives origin
from the verified session's tagged authority and capability parent. Policy and
member issuance use the same predicate. `ValidateAuthority` exposes the earliest
expiry, future revocation deadline or binding deadline in the complete live
ancestry. No new persisted origin flag or second authority registry is needed.

The two generated session HTTP regressions first failed with a detached root
and accepted machine consent. The lifetime variant then failed with a session
expiry beyond the original token. They pass after the fixes. The real SQLite
test logs in with an expiring token, mints a child through the actual controller,
checks its session parent and expiry, then revokes the original token and
confirms the child becomes unusable. W1's direct-store bypass remains reproduced
in the handoff patch.

DESIGN: committing origin gate. W1 must call the same direct-session predicate
in member-root provisioning and machine-consent transactions after resolving
current authority. HTTP checks alone do not close either store entrypoint.

DESIGN: trusted origin admission. W1/W6 must persist and validate schedule
consent and verified App delivery facts in the sole admission owner. Then W2
can route both callers through it and delete triggerIntake/triggerRows and the
old insertion calls. See W2-review-intake-handoff.md.

DESIGN: production dependency composition. W1 must implement the declared
credential, bootstrap and HasDeploymentAdministrator, local peer, launcher,
account, team, schedule, approval, metrics, billing and concurrency contracts.
W1b must settle non-checkout proofs. Data signing needs the native authority
projection and tenant access dispatch. Make these concrete composition inputs,
with compile-time conformance and route-through-commit checks, once their
owners provide them. No new optional adapter was added to conceal the gaps.

DESIGN: native declared-reference relation. W1 must check the live bound job's
sealed PipelineReference declarations for Target.Pipeline, alongside existing
same-run dependency relations. Substituting run/job selectors cannot implement
this missing relation. See the native reproduction patch.

DESIGN: local-first matching. W1 must use one store-clock policy for default
preferences, hold and liveness across named, queued and self claims, using
current enrolled eligible contenders and accepted observations. W2 can wire
the existing settings after that input is declared. No placement feature or
flag was cut.

DESIGN: required serving and log composition. W1/W3/W4/W7/W9 must declare the
missing authority-bearing operations and final log request/commit/signing
shapes. The exact 23-route inventory and per-family proposals are recorded in
W2-review-route-coverage.md. The completeness gate stays red until real handlers
exist. No dummy handlers or policy-row deletions make it pass.

DESIGN: dashboard source resolution. The manual form has a GitHub URL and branch,
but the final request requires a resolved numeric repository identity. W1/W6
must resolve those inputs through the intake owner and seal its acquisition
location and commit. The UI forwards source input and repository display without
inventing a canonical ID or App trust. Its decoder/transaction-handoff test does
not claim successful native admission. W1/W2 also need the final debug-resume
transaction behind the already-declared jobs/{job}/resume policy.

## Test repairs, deletion and budget

Deleted the 201-line AST scope guard and its eight-line exported parser helper.
The unrelated 21-line approval assertion survives in its own test file. Two
scope-enumeration callers now use real registrations and final policy metadata.
The tautological dummy route-registry test is removed. The reporting property
no longer replaces successful handlers or claims a permissive binding fake
proves native reference reads. The trigger test asserts final route, envelope,
idempotency and nested source fields instead of accepting the retired body.

The dashboard Go test invokes the real TypeScript callers, captures their
requests and passes them through actual controller routing/decoding without
sockets. It first reproduced all five reviewed route/body failures. Mutations
reach an explicit transaction fake. Metrics/pause reads prove registration,
not production data availability. Separate frontend tests cover error propagation
and the final resume request. No native acceptance cell was newly unskipped.

W2 production Go delta for this session: +104/-108, net -4. Against main:
+13,280/-16,997, net -3,717, within the -250 budget. These counts exclude tests,
testdata, controller/client and contract declaration files. Shared apiroutes and
frontend changes are outside those ownership counts.

Mechanism added: registration recording replaces AST inspection because source
variable names and policy declarations cannot prove what the serving mux
registered. The smaller declaration-only comparison already missed these gaps.
Removing the recorder restores the old test weakness. No runtime setting,
persisted authority field, new environment variable or compatibility alias was
added.

## Verification and remaining evidence

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false, at most four package
workers, and cache/temp directories under /tmp/sw-astra-W2. No Git write or
Sparkwing executable ran.

Passed:

- Full `go build -p 4 ./...`.
- Vet for capability, apierr, apiroutes, controller and tests/acceptance.
- Full capability, apierr and apiroutes suites.
- Focused controller reporting rejection, route-policy/drift, metrics authority,
  login limits, telemetry, retention and HTTP claim tests.
- Focused acceptance credential policy, issuance, login/session flow,
  derived-session properties, real SQLite descendant and dashboard request tests.
- All 15 focused frontend API tests, plus frontend ESLint on the changed API files.
- commentcheck against main, documentation mirror checks and git diff --check.

Still red: TestFinalCredentialServingRegistrations and
TestControllerRegistersDeclaredLogSubmissions, covering the 23 omissions.
The cross-owner native reproduction tests are preserved as
W2-review-reproductions.patch instead of being silently skipped.
No full controller or whole-repository test pass is claimed. Socket-based
exposition/listener tests cannot run in this sandbox.

Shared edits: internal/apiroutes and its tests; tests/acceptance credential
fixture and new HTTP regressions; web/src/lib/api.ts and api.test.ts;
authentication/observability source and mirrors, changelog and embedded copy;
C2 credential documentation. No lead-owned
handoff or amendment, store implementation, foreign wire type or Git state was
changed. The three W2 review reports and reproduction patch contain the lead's
remaining routing work. Scratch and all task caches are deleted at handoff.

STATUS: COMPLETE
