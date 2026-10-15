# W9 continuation handoff

W9 remains incomplete. This continuation starts at `83dd6fcd6`. Earlier work is
committed. This continuation is uncommitted for the lead to review and commit.
No Sparkwing executable, service, external file or repository Git metadata was
changed. The assigned environment was reused. Scratch is removed before the
final report.

## Earlier work retained

Earlier commits supply final grammar, help/completion/discovery, local run and
trigger parsing, machine-only connection profiles, authenticated engine
transport, public run observations, listing/prune/log/review adapters,
statistics, sizing reads, pools, separate team/deployment token managers,
dashboard lifecycle adapters, staged upgrade verification, typed secrets,
runner observations and confirmed member-token revocation for profile removal.

The previous continuation supplies typed schedule management and repository
installation/withdrawal over the final `/schedules` routes. It preserves exact
empty/zero overrides, resolves selections before mutations, retains authoritative
upcoming/history projections and uses idempotent immediate-run requests. Wire
models moved from `internal/api` to `internal/controllerwire`. Production CLI
schedule management no longer uses `/crons`. Local install/pin/tick still uses
the older runtime. Do not redo these completed pieces.

## This continuation: runner credential integration

Runner add/remove now parse the shared `clisurface` grammar. Repeated and
comma-separated `--label` and `--repos` values survive. The registry records the
already-required runner name and existing positive concurrency requirement.

Enrollment uses `TokenManager.CreateToken` in the team namespace. It requests
kind `runner`, a display principal and the exact existing runner grants
`nodes.claim`, `triggers.claim`, `runs.state`, with one idempotency key. The
permission name `triggers.claim` remains part of C2 even though its public route
is the runner run queue. No grant was renamed or widened.

Removal validates the enrolled controller and optional runner name before any
service action. It reads explicit kind from `TokenManager.ReadToken`, stops the
service and then uses confirmed `RevokeToken`. Principal spelling supplies no
kind. An `agent:NAME` principal cannot cause a member credential to be revoked.
Stop failure leaves the credential live. Revocation failure emits no success
receipt and retains the saved config.

JSON receipts include safe metadata and service status, without a raw bearer or
mixed pretty progress. Plain emits the runner name. Failure after issuance names
the live prefix and final revoke command on stderr. The existing file/service
plumbing remains until W6/W8 composition. It still writes `agent:` with a token
and renders the older service command. This is not accepted native-runner proof.

`--reserved-for` has no registration/provisioning request in the supplied
contracts. `TokenCreate` cannot request pool reservations. The CLI returns
`policy_missing` before issuance for that selection. W2 must provide the
team-admin registration seam. This is an incomplete integration, not an approved
feature cut. No C2/W6/W8 contract was changed.

## Deleted

This continuation deletes **316 tracked Go lines gross** against its starting
HEAD, including replaced handlers and test fixture code. This is not a net
reduction claim. The hard API cut removes 53 lines from client/admin.go:
`Client.CreateToken`, `Client.LookupToken`, `Client.RevokeToken`,
`CreateTokenRequest`, `CreatedToken` and `TokenInfo`. Their final production CLI
caller moved to TokenManager. No outside-package caller migration was needed.
The legacy mint wrappers, principal-kind inference, duplicate flag parser and
unstructured credential reply decoding are removed from the runner CLI.

The 1,610-line authentication guide is replaced with a 335-line page named
Authentication design and an implemented credential-management guide. Historical
section anchors remain. The design retains account, payment, billing, secret,
claim and storage concerns while deleting obsolete token families, cache-service
credentials, admin bypasses, Argon2 capability lookup and removed CLI examples.
Target guarantees are marked as design. The new reference documents the client
surface separately. Grant-vocabulary drift is checked against capability.Grants,
and examples parse through the shared grammar. Embedded copies, sidebar, runner
command reference, changelog and the client API snapshot are updated.

## Tests and acceptance

`TestSurfaceRunnerEnrollmentCredentialTransport` passes for api.sock and remote
connection fixtures through the real authenticated HTTP client. It covers typed
runner issuance, exact grants, header idempotency key, server-derived metadata,
selected metadata read and confirmed revocation. It is a transport proof. It
does not prove actual issuer authority or native enrollment.

Ported all 16 original runner/prefix tests to final flag spellings and a fake
TokenManager, retaining real private-file writes and fake service execution.
They retain preflight-before-mint, symlink/overwrite refusal, force replacement,
contribution/repository validation, raw-token non-disclosure, manual supervision,
service failure recovery and non-runner-kind refusal. Nine further tests cover
repeatable selection, idempotency input, JSON receipts, controller/name
preflight, pool-contract refusal, stop-before-revoke, stop failure, revocation
failure and refused issuance. T1's parked source inventory assigns no original
function directly to W9. Native product surface cells remain skipped.

Passed on this continuation:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` for cmd/sparkwing, clisurface, controllerwire,
  controller/client, tests/acceptance and pkg/docs.
- Full clisurface, controllerwire and tests/acceptance suites, retaining pending
  native product scenarios.
- Focused CLI/client tests matching `^TestRunners|^TestTokenPrefix|^TestAuthGuide|
  ^TestCredential|^TestTokenCommands|^TestProfile|
  ^TestCommandReferenceMatchesRegistry`.
- Pkg/docs mirror/changelog/link-rendering checks, sync-docs --check, commentcheck,
  sleepcheck and git diff --check.

An initial command-reference drift failure was corrected by regenerating the
reference from the test renderer. No product executable ran. The temporary
renderer test is deleted. API generation ran into scratch and only the client
snapshot was copied back, so unrelated snapshots did not change.

Go verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-astra-W9/cache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W9/tmp.
Recreate those children to repeat checks. Tests use -p 4 at most. Pipelines,
full legacy CLI/client suites, server/socket/service execution and native
Linux/macOS/Windows surface cells remain for the lead.

The preceding session's internal/crons suite failed the final schema's
cron_schedules.repo_key requirement and inherited GPG signing in Git fixtures.
That suite was not rerun here and remains unverified.

## Work still required

1. Compose W3 hosting with W6 sealed requests/settings. Publication, cache-from,
   target selection, here resume and pipeline-ref acquisition remain unfinished.
   Detached execution still starts the removed consumer entrypoint. Move it to
   the local-controller queue, then delete consumer/process helpers.
2. Compose W8 lifecycle/runtime/managed units/logs. Finish hidden wingd arguments,
   update refresh/restart, doctor, local schedule install/pin/uninstall and
   __tick. Delete direct-store schedule paths after composition.
3. Implement W2's final schedule projections and W1 identity writes, including
   aliases, requested history/upcoming, masked arguments, empty/zero override
   presence, no clamping and idempotent launch. Port the remaining legacy cron
   integration tests and delete the old Client cron API with its callers.
4. Compose W6's complete machine schema, private credential directory and W8's
   native runner service/start/refresh. Delete agentFileConfig, agentconfig and
   agentservice use in the CLI. The typed credential portion is now finished.
   Integrate W2's runner registration, grants and --reserved-for proposal.
5. Replace profile.Load/Save and connection selection with W6's final machine
   parsing/persistence. Profile scope enrollment remains a W2 gap.
6. Migrate queue, cache warm, remaining legacy client APIs and management paths.
   Finish remaining guides, scripts, examples and runtime tests that still use
   removed vocabulary/schemas. Auth's obsolete guide is replaced here.
7. Execute every native surface cell and launch QA after owner integration.

## Contract changes needed in other areas

- W1/W2: failed/all job retry selection and an authorized sizing reset that
  clears learned samples/floors while retaining explicit resource pins.
- W2: ordered mixed trigger/job claims, profile scope enrollment, run-scoped
  annotations and ordinary-member annotation authorization.
- W2: explicit team-admin runner registration/provisioning with requested pool
  reservations. TokenCreate has no such proposal field. Issuer metadata must
  retain granted reservations and repository authority separately from labels.
- W8: runtime/unit construction, canonical logs, access-policy/install-force
  fields and effective endpoints. ManagedUnitManager has no schedule
  install/uninstall method. LocalRuntime exposes only Run. A stable tick needs
  one controller-owned scheduler-pass seam. Runner enrollment also needs its
  final machine config/private-file and supervised-unit composition.

Real integration also needs W2's C3 server projections and apierr.DecodeInner,
W1/W2 encrypted/restricted-secret persistence and enforcement, W7 persisted
logs/cursors/final completeness and W3/W6/W8 hosting. These are not reasons to
restore the removed APIs. No new human decision, environment variable, service,
timeout or product restriction is proposed.

## Shared files and friction

No W1-W8 production file changed this continuation. No outside-package caller
migration was necessary for the removed token client methods. Shared changes are
CHANGELOG.md, .apidiff/pkg_controller_client.txt, the C3 notes, this handoff,
tests/acceptance/runner_credentials_contract_test.go, pkg/docs/changelog.md and
the generated documentation mirrors. Other source changes are W9-owned.

Friction: final runner provisioning and machine/native lifecycle seams are
unfinished. Native execution is outside this worker's authorized verification
path. Discovery tools report an older installed Overwing binary, but standards
matching/showing worked. Git identity remains Korey Gambill and core.worktree is
unset. The lead owns review, commits, pipelines and landing.
