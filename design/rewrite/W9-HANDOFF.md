# W9 handoff

W9 remains incomplete. This continuation started clean at b75cf16a2. No Git
writes, Sparkwing executables, services, pipelines or releases ran. The lead
owns commits, independent review and native integration. Earlier implementation
and verification details are in this file's Git history through b75cf16a2.

## This continuation

Committed-source preparation preserves separate execution and pipeline commits.
The execution commit supplies configuration and Git.SHA. Its configured source
or the explicit pipeline revision supplies Source.Ref. A declared source pin
and explicit revision must resolve to the same commit. The execution commit's
invoked name/SDK entrypoint pair survives pipeline-source selection and dirty
configuration. Git discovery and preflight share the same captured Git process
projection and native reader.

Committed-source preflight requires origin's remote-tracking refs to contain
both selected commits. An unrelated remote cannot satisfy it. This ports the
existing pushed-source preflight expectations and adds divergent pipeline and
execution history. The legacy preflight tests remain with their connected caller.

Inspection calls source.Acquire for the exact pipeline commit through its declared
interface. It carries repository identity, location, default-branch hint and the
captured process base. The result must preserve repository/commit identity and
supply complete selected-engine metadata for the requested SDK and target. W3/W8
still own pin resolution, signature verification and acquisition implementation.
The selected engine's pipeline describe command runs in the acquired directory.
Its returned alias and entrypoint must match the captured pair before argument
metadata is accepted. The request preparer and existing final-intake component
consume that metadata in a fake-controller test.

Acquisition resources close on success, error and cancellation, including a
partially returned entry and cancellation during cleanup. Policy notices pass
through the existing masked writer after environment registration. A failed write
stops inspection. The existing process
builder and CLI sanitizer filter credentials and mask inspection diagnostics.
A Unix shell fixture proves argv, cwd, captured environment, credential filtering
and secret classification through the real subprocess boundary. It is a fake
engine, not a verified release or native SDK integration.

The public initial trigger still uses its legacy preparation/intake/here path.
No public flag, wire field or foreign contract changed. No new service, cache,
environment variable, timeout, retry loop or authority rule was added. The private
inspection dependencies reuse the source interface and existing command executor.
Native selected-engine process-tree cancellation remains unverified.

## Completed W9 work to retain

- Final clisurface grammar, help, completion and command discovery. Machine-only
  profiles use the strict userconfig schema. Final run/trigger flags are parsed.
- Authenticated engine HTTP/api.sock transport, strict errors, one-time authority
  retry rules, claims, reconciliation, complete failure reports/recovery reads,
  expansion snapshots, result-state identity and cloud award/bootstrap models.
- Run status, paginated listing/tree/steps/receipts, deletion, logs/search/review,
  retry/cancel, statistics, sizing reads, pools, tokens, secrets, runner reads and
  queue/group priority adapters. Fakes cover the final request/projection shapes.
- Sealed here handoff/resume, private retained bearer, controller/socket binding,
  public logs and incomplete-observation results. Native here construction is open.
- Dashboard, daemon control, doctor, foreground wingd and machine-update adapters
  consume final lifecycle contracts. Their native local-services binding is open.
- Typed schedule management preserves explicit empty/zero values, upcoming/history
  projections and keyed launches. Local install/tick composition is open.
- Runner enrollment/private-file and startup-schema adapters, typed issuance and
  removal. Native provisioning/service composition and pool proposals are open.
- Selected-engine upgrade verification, hidden SDK installer, Go/TypeScript local
  preparation, binary-cache hard cut, installer masking and cache warming.
- Protocol v2 schemas, HTTP policy/OpenAPI references and corrected source guides.
  OpenAPI still contains legacy registration inputs and undocumented operations.
- TypeScript Windows bridge build/assembly/publication workflow and immutable npm
  publication checks. Native Windows execution and publication remain unverified.

## Remaining W9 work

1. Connect public initial trigger to source discovery, inspection, request
   preparation and submitTriggeredRun. Bind numeric GitHub identity, captured
   private environment retention and authorized working-tree source upload.
   Initial here intake needs the persisted reason contract below. Delete the
   legacy CreateTrigger/TriggerResponse and old here host after the cutover.
2. Replace local and detached execution with W3's shared RunHost and W8's local
   controller queue. Compose owner actor, claims, source, exact engine, storage,
   plan cache, lease keeper, final logs/signals and target selection. Delete the
   old local host, detached consumer and direct-store plumbing after migration.
3. Compose explicit read-only RemoteCache and publication byte/record copying.
   Publication failure must remain separate from the completed local outcome.
   Retire remaining old storage/client consumers after their callers move.
4. Bind resolveHereRuntime and resolveLocalServices to W3/W8 implementations.
   Preserve controller/socket identity for retained here authority. Complete
   native lifecycle endpoints, managed units, logs and runner startup/refresh.
5. Finish local schedule install/pin/uninstall, captured schedule intake and
   controller-owned __tick. Integrate W1/W2 schedule persistence and projections.
6. Finish remaining legacy client/management retirement and the runtime-guide,
   script/example and OpenAPI audit. Regenerate owning references when public
   behavior changes. Run every native surface/placement cell and launch QA with
   the lead after the owners are implemented.

## Cross-area contracts and bindings still open

- W6/W1/W2: a typed here reason must survive immutable intake and replay.
  CreateRunRequest, IntakeRequest and runspec.Request still lack it.
- W1/W3/W9: job transitions need the source attempt ordinal in the wire request,
  committing transaction and replay identity. The existing handoff still applies.
- W6: procenv.Git is declared, but Build rejects it. Git fixtures inject the
  declared builder boundary. W9 has no alternate environment filter.
- W3/W8: source acquisition, exact-engine resolution/InstallSDK, shared host
  constructors, execution-input identity through hello and local lifecycle are
  declared stubs. Inspection fakes do not establish their native behavior.
- W1/W2/W7: authorize pre-intake source_bundle upload and publication result-copy
  operations. Existing upload identities require a live owner/publication/job.
- W1/W2: retain RetryRequest.Mode in intake, replay and successful-job reuse.
  Specify authorized sizing reset and preserve explicit resource pins.
- W2: profile scope enrollment, pool-reserving runner provisioning, run/member
  annotation authority, remote admission priority without a group and final read
  projections remain owner integration or route-contract gaps.
- W8: managed schedule installation and controller scheduler-pass operations,
  dashboard access/install-force policy, native endpoints and unit refresh remain.
- W7: native remote-cache/storage bindings and final persisted log completeness.
- T1/lead: native HTTP/api.sock, platform/backend/placement drivers, verified
  engine/SDK execution, Windows packaging runs and real-repository launch QA.

These are unfinished integrations, not approved feature cuts. No foreign
contract changed and no new human decision is requested in this continuation.

## Assignment audit

Every amendment and handoff naming W9 was checked. Previously completed work was
retained. The rows below group the remaining bindings without claiming native
acceptance from component tests.

| Assignments | W9 result and remaining integration |
| --- | --- |
| A1-03/08/09/13-18/22/38/55 | Final error/claim/execution/failure/expansion/cloud wire retained. Native owners and source-attempt fencing remain. |
| A1-10/11/19; identity and alias handoffs | Separate commits, preflight and selected-engine inspection added. Native source/engine/host construction and hello remain. |
| A1-20/39/40/41/49 | Credential, runner and lifecycle adapters retained. Native provisioning, shared runner, local runtime, services and timers remain. |
| A1-29/30/34/37/50 | Signed-data, local-cache, here and prepared-intake components retained. Public cutover, cache/publication and old runtime/client removal remain. |
| A1-43/45/47 | Notices, upgrade, settings and guards retained. Inspection now renders selected-engine notices. Native version policy and scheduled intake remain. |
| A1-51 | C3 and eight intake transport combinations retain separate source commits. Final public/runtime retirement and reference regeneration remain. |
| A1-52; Windows packaging handoff | Packaging implementation retained. Native Windows and publication evidence remain unclaimed. |
| A1-53 | New native Git and shell-fixture evidence only. No native T1 scenario was unskipped. |

## Evidence and accounting

Invariants: source selection and inspection cannot substitute the captured
execution SHA, pipeline commit or invoked name/entrypoint pair. Registered proxy
credentials cannot appear in inspection errors, hints, detail values or keys.

Seven new test functions cover revision/pin selection, origin preflight,
acquisition inputs, twenty failure/cancellation cases, final intake and the
native shell boundary. Two new fuzz properties passed generated runs with four
workers and three requested seconds: 6,856 identity and 4,223 masking executions.
The masking
property first reproduced raw notice output, then passed 4,223 generated cases
after notices used the shared stream and the process base registered masks first.
The preflight expectations port TestCommitOnOriginSeesOnlyPushedCommits and
TestOfferTriggerSourceRequiresPushedCommit. Their legacy callers remain connected.
T1's parked inventory assigns no original test function directly to W9.

TestSurfaceInitialTriggerIntakeTransport passes eight fake-transport combinations:
HTTP/api.sock, ordinary/here and shared/separate pipeline commits. No native
scenario was unskipped. No legacy code was deleted: 0 lines. The source reader's
replaced closure is a refactor, not runtime retirement. No foreign production
caller migrated. The repeated historical handoff was consolidated here.

Checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4 and cache/tmp paths
under /tmp/sw-astra-W9. Tests use -p 4 or less. Whole-repository build, scoped vet,
focused CLI/client tests, full clisurface/controllerwire tests, TestSurface,
TestProtocolSchema, commentcheck, sleepcheck, gofmt and diff checks pass.
Full legacy CLI/client suites, services/sockets, verified releases, native SDKs,
Postgres/Darwin/Windows, pipelines and launch QA remain unverified.

Shared files touched are tests/acceptance/trigger_contract_test.go,
contracts/c3-controller-wire.md and this report. Other changes are W9 CLI source
and tests. Friction remains the missing Git environment and native owner
bindings. Overwing discovery worked with its stale-binary notice. Scratch is
removed before the final report. No Git write occurs.
