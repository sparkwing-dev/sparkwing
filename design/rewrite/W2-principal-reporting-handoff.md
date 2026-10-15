# W2 Principal and reporting continuation

<!-- design:begin -->

Started from 2ce0338f1. No Git writes. Lead-owned handoffs.md and amendments
are unchanged. This report continues W2-browser-console-handoff.md.

## Implemented and deleted

Deleted auth.go and its Principal, context accessor, HasScope, Scope constants,
old error body and helper functions. Deleted the default-team request fallback,
requestTenant, runTeam, unused egress wrappers and unused live-log HTTP handlers.
No production controller code reads the old request identity. Existing internal
lifecycle reads still select an explicit tenant. Dashboard live-log reading and
streaming remain active. Constant-only callers use their existing fixture scope
strings. No production verifier, route alias or credential compatibility path was
added. A scan found no unreferenced production handle* method in the controller.

Registered the final job metrics GET/POST and usage POST routes. The POSTs pass
Authorized to MetricWriter. The raw metric/usage methods are not fallbacks.
Missing or foreign writers refuse with policy_missing. Metric kinds, resource
values, interval CPU-time constraints, timestamps and usage bounds are checked
before the writer. The metrics GET uses the selected tenant and job and preserves
pagination. Go client callers use the final /jobs/ paths.

Removed the old Principal-based runner-budget wrappers. Actual policy handlers
apply the existing token budget after authorization. Claim and heartbeat budgets
use the authenticated team/authority and holder. A forged holder header cannot
create another heartbeat bucket. Only an unbound root may supply a runner name.
Empty final claim responses spend the poll budget. Awards do not and clear idle
advice. Existing limits and defaults are preserved. Refusals retain warning logs,
metrics and Retry-After.

Five deployment-setting writes pass Authorized to OperatorSettingsWriter.
The old settings-write methods are removed from the HTTP dependency interface.
Signup mode and limits reach one committing request, preserving omitted patch
fields. Quota/allowance writers return the committed projection. Missing adapters
return policy_missing. C2's new local contracts and exact W1 obligations are in
contracts/c2-credentials.md. No C1 signature or implementation changed.

Audit records use final run/job path identifiers and skip successful empty final
claims without the retired pending-offer header. The legacy audit credential
helpers and offer-only machinery are deleted. Egress concurrency failures use
rate_limited with a reason under error.details. The exported old egress-code
constants are deleted.

The implemented declarations moved out of five *_contract.go files into
internal/capability/authentication_types.go and management.go, and
pkg/controller/policy.go, engine.go and data.go. Long declaration comments were
cut to one-line GoDoc. W9's client declarations are unchanged.

SyncLauncher now also resolves LauncherSyncReader from Store.AsOperator when
no explicit reader is supplied. Its existing authority/fence forwarding and
bearer stripping remain. W1 still needs the actual fenced reader.

Production Go counts use the lead's ownership/exclusion rules, include untracked
files and count declarations after their move into real files. This session adds
593 and deletes 764, net -171. W2 versus main adds 13338 and deletes 15971,
net -2633. The W2 budget is -250.

## Invariants and ports

Invariant: a metric or usage write affects only the authorized tenant/job and
rechecks current authority and holder/attempt fences in its committing
transaction. TestMetricsHTTPAuthorityProperty generates selectors, missing grants,
revocation, committing conflicts, private failures and missing/foreign writers.
ValidationAndLimit covers invalid bodies and metric-limit mapping. The timestamp
port preserves malformed, explicit, omitted and receipt-time cases and adds
nanosecond-range refusal. ReadsPagedSamplesFromAuthorizedTenant exercises actual
SQLite reads, cursors, limits and foreign-job refusal with a fake authentication
snapshot. Seed rows do not claim production write coverage.
TestMetricAndUsageClientsUseFinalJobRoutes ports the transport round trip with a
fake RoundTripper and preserves command CPU accounting and timestamp values.

Invariant: each settings transaction rechecks deployment authority, and a signup
request commits its mode/limit changes together. The existing round-trip,
invalid-body and private-failure tests use a fake committing writer.
CommitAuthorityProperty generates hostile selectors, configuration changes,
revocation, committing refusal and missing writers and checks exact authority and
target forwarding. This is HTTP/component evidence, not a proof of W1 atomicity.

The default console adapter now has a real SQLite account-session property test.
TestServerPolicyConsoleUsesDirectSessionAndConfiguredGate exercises hashed
session lookup, encrypted CSRF-key persistence, current reader membership,
configured operator selection, CSRF, origin and presentation precedence through
the actual handler. The committing console writer is fake. Missing roleless
session and committing-gate integrations are not counted as complete.

The socket-based request-budget tests and old claim-budget test are ported to
real authenticated /runners/claim requests over SQLite without opening sockets.
They retain poll charges, free awards, independent names, name caps, unlimited
defaults, fleet cadence and idle-advice/enforcement cases. The heartbeat property
varies headers under one authenticated holder. The obsolete trigger-column
fixture is removed. The advice-reset test exercises recordQueueActivity directly.

Final-policy audit tests replace the old token-table identity fixture tests.
The generated privacy test checks that no secret path, query, raw credential or
raw user agent reaches logs. Other ports cover sensitive reads, authenticated
denials, forged credentials, path IDs, request-ID replacement and empty polls.
Existing span and budget-key tests use capability.Principal. The concurrency
refusal test checks the canonical envelope before invoking a log handler.

The old tenant fallback/runTeam tests retire with their helpers. Final route
properties retain tenant selection. The direct-download-wrapper test retires
with that wrapper. TestDataReadHTTPDownload and DownloadFailures cover the final
charge-before-sign boundary. Six TestStoreDataDownload* tests retain actual
replica accounting, capacity races, isolation and rollback. No native credential
matrix or placement cell was newly completed. The port inventory records these
limits. No scenario catalog, Harness or boundary registry was extended.

## Contract changes needed and remaining work

- W1 implements MetricWriter and OperatorSettingsWriter as specified in C2.
  Recheck authority and actor inside the transaction and keep all effects atomic.
- W9/W1 must settle usage-report identity. The retained client sends additive
  per-process usage, but its body has no process-report identity. Identical valid
  process reports cannot be distinguished from exact-body retries by the declared
  inputs. W2 did not change the foreign wire shape or claim replay-safe durable
  usage writes. Omitted metric timestamps also need a durable replay/receipt-time
  rule. Production defaults refuse absent writers.
- A real metric-seeding probe found a separate W1 legacy writer mismatch:
  Tenant.AddNodeMetricSample uses ON CONFLICT(run_id,node_id,ts), while schema.sql
  keys node_metrics by (team,run_id,node_id,ts). SQLite returns
  "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint".
  The final read test seeds rows directly and proves reads only. W2 did not patch
  the foreign writer. The new authority-bearing writer must use the final key.
- W1 ReadAuthentication still rejects CredentialPeer and has no trusted local
  mutation-authority representation. Server.HTTP and the verified peer transport
  remain implemented against the contract and tested with fakes. No root or
  permissive local identity was fabricated. Roleless sessions and the configured
  console committing gate remain W1 work.
- W1 authentication_binding.go still does not project BindingState.CancelReason.
  C2 carries the field. EngineController.Renew already forwards the cancellation
  reason returned by C1 RenewUnit. Project first job intent before run intent in
  the one authentication snapshot when W1 supplies it.
- W1 still lacks EnsureBootstrapCapability and HasDeploymentAdministrator.
  The seeded bootstrap must enter Store.Issue's gated, digest-only, once-only path.
  W1b SettlePayment still refuses the verified card/refund/warning/dispute kinds.
  W2's provider verification and startup contracts remain tested and implemented.
- W1/W8 must supply the real launcher reader, immutable award/claim-generation
  fence and Kubernetes Job-instance correlation. W2's default reader hookup is
  ready, and sync never returns bootstrap bytes.
- Prior W1 credential, source, data, account, enrollment, sizing, concurrency,
  schedule, run-control and business committing integrations remain listed in the
  earlier handoffs. Broad old socket fixtures that use removed token/store APIs
  still need porting or deletion. W2 and native acceptance remain incomplete.

Mechanisms added: two narrow committing interfaces, the existing budget's
post-policy handler wrapper, and the audit record's empty-poll flag. Raw writes
would bypass the frozen authority contract. The flag avoids parsing a serialized
claim response or restoring the retired offer header. No new environment variable,
cache, timeout, schema, customer limit or default was introduced.

## Verification

Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache and GOTMPDIR/TMPDIR=/tmp/sw-astra-W2/tmp. Commands
select this checkout with go -C. Tests use -p 4 and -timeout=180s.

Passed:

- Whole-repository go build -p 4 ./... on the final production source.
- Scoped vet for controller, controller/client, capability, apierr, executable
  acceptance, sparkwing-controller and the browser fixture.
- Full capability and apierr tests.
- Controller selection, 10.129s, plus the final paged-read/sync selection, 0.171s.
  The broad selection is the Test prefix set from the previous W2 report plus
  ServerPolicy, MetricTimestamp, MetricsHTTP, OperatorSettings, ControllerEgress,
  StampPrincipal, Audit, RequestBudget, ClaimBudget, ClaimPollAdvice and the
  final HandleClaimNode/Trigger advice tests. FuzzEngineLauncherSync seeds pass.
- Executable W2 acceptance selection from the prior report, 13.719s.
- Focused client transport selection including the new metric/usage port, 0.030s.
- Six TestStoreDataDownload* regressions, 2.654s.
- Native Linux TypeScript: go test -p 4 ./sdk/typescript/test/nativehost
  -run '^TestNative' -count=1 -timeout=180s, 8.469s. No setup failure.
- Full apiref, apispec and API-snapshot generator package tests.
- Focused docs mirror, changelog, exported-symbol and privacy checks.
- API snapshot regenerated. Documentation mirrors synced and --check passes.
- commentcheck -base main . is clean across 2199 Go files. git diff --check
  and bin/check-changelog.sh pass.

The full controller suite still includes old socket fixtures and removed store
APIs. The full docs suite also requires sockets. The sandbox refused one broad
metrics selection at TestMetrics_ObjectStoreBudgetIsExported with
"listen tcp6 [::1]:0: socket: operation not permitted". Focused tests use in-process
HTTP or net.Pipe. No full-suite or native deployment pass is claimed.

The lead owns pipelines, candidate runtime, review, landing and release. None ran
here. Default Git identity remains Korey Gambill and core.worktree is unset.
Session scratch is deleted at the end. The shared Go cache is retained.

## Shared files touched

Go caller migrations outside W2:

- cmd/sparkwing/crons_profile_test.go
- internal/orchestrator/event_payload_mask_test.go
- internal/orchestrator/secret_args_execution_test.go
- internal/orchestrator/trigger_host_here_test.go
- internal/orchestrator/trigger_host_test.go
- internal/web/browserfixture/main.go
- pkg/controller/client/client.go
- pkg/controller/client/coordination.go
- pkg/controller/client/coordination_test.go
- pkg/controller/client/metric_routes_test.go

The first six files and coordination_test.go only replace removed controller
scope aliases with the same fixture strings and remove unused imports. The two
client production files migrate metric/usage route paths. The new client test
covers those transports. Other shared artifacts are C2's own contract, the port
inventory, auth/migration docs, CHANGELOG.md, generated mirrors and the controller
API snapshot. No W1 or W8 source file or lead-owned contract file changed.

<!-- design:end -->

STATUS: CONTINUE
