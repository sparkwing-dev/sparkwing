# W1 continuation 21

W1 remains incomplete. This continuation retires timestamp heartbeat writers and
the independent recovery path that consumed them. W1b implementation files are
unchanged. HEAD remains b87fb42f15ba2e00320dd6a61d7bc6ae57b6a505. No Git write was
attempted.

## Removed and retained

`TouchRunHeartbeat` and `TouchNodeHeartbeat` are removed from Store, Tenant,
Client and StateStoreOld. Every Go caller is removed or migrated. The old run
heartbeat and node-touch HTTP handlers, route registrations, tenant-route entries
and hosted retry entries are removed. The scheduler's independent run ticker
and the Kubernetes dispatcher's advisory node ticker are removed with their
launch sites. Pod claim renewal remains responsible for pod liveness.

`Run.LastHeartbeatAt`, its SQL writes, scanner field and API schema property are
removed. The final schema already excludes that column. The job heartbeat field
remains because final claim renewal updates it. Doctor's independent
heartbeat-age run finalizer, grace constant, daemon-run lookup and
`orphaned_runs` report field are removed. Owner expiry uses the elected
`ReapUnits` pass.

The Go diff removes **541 existing lines**, including **327 source lines**
and **214 test lines**. It adds **121 lines**. The net Go reduction is
**420 lines**. Markdown, API YAML and generated mirrors are excluded.
No production mechanism, setting, environment variable, compatibility shim,
customer limit or schema migration is added.

## Invariant, ports and acceptance

The invariant is: only the exact live owner lease keeps the run owner alive,
and activity under a remote job claim cannot extend it.

`TestOwnerLeaseAloneDeterminesLivenessProperty` generates 24 timelines with
varying renewal counts and clock advances. Real ClaimUnit and RenewUnit calls
prove live-owner preservation, exact-boundary lapse, failed-renewal atomicity,
unstarted requeue without disruption, and preservation of a separately renewed
remote claim. The test uses the final schema and an injected store clock.

The SQLite unstarted-owner proof now checks that RenewUnit persists exactly the
returned lease before recovery. Its existing repeated requeue/reclaim, stale
plan/renewal refusal and reopening checks remain. All eight SQLite recovery
cells pass: cancel-beats-loss, reconciler-waits-and-reclaims, unstarted-lapse,
started-lapse-cap, concurrency-continuous-adoption, concurrency-no-revival,
application-retry-ordinal and local-orphans.

The old Doctor orphan/grace tests retire with that mechanism. The local-orphan
cell retains started/unstarted policy under the elected reaper. The two old
Kubernetes advisory-loop tests retire with that loop. Final store renewal tests
retain revoked, expired and stale-fence refusal. Host shutdown on renewal loss
still requires W8's runtime integration. No native proof or additional archived
T1 source-function identity is claimed closed.

The PostgreSQL contention test now races its existing activity mutation against
execution starts. It is renamed TestPostgresConcurrentExecutionStartsAndActivity.
It remains a legacy fixture and was not run. The local API contention fixture
uses its existing activity route, with its sample labels updated together. The
node ordering fixture retains reverse-order StartNode updates. These are minimal
caller migrations, not final native ports.

## Verification

All Go checks use GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4, -p 4, and
GOCACHE/GOTMPDIR/TMPDIR below /tmp/sw-astra-W1.

- Whole-repository go build -p 4 ./... passes on the final source tree.
- Scoped go vet -p 4 passes for pkg/store/..., pkg/controller/..., pkg/storage/...,
  internal/nodehost/..., internal/orchestrator/..., internal/opsview/...,
  internal/runners/k8s/..., internal/acceptance/..., tests/acceptance/... and
  cmd/sparkwing/.... The final contention-label edit also passes scoped vet.
- The W1 selection passes 400 top-level tests / 1,143 cases, with no skips.
  Select the Test functions from w1-session21-tests.txt, omit TestPostgres names
  and run their anchored alternation against ./pkg/store. The final rerun passes.
- The final owner-liveness property plus all TestRenewUnit tests pass separately.
- All eight SQLite recovery cells pass on the final source tree with
  go test -p 4 ./internal/acceptance ./tests/acceptance
  -run '^TestRecovery$/[^/]+/store-sqlite$' -count=1.
- The three Engine claim/trusted-service/bootstrap adapter tests and the pure
  Doctor plain-renderer coverage test pass. Nodehost tests compile without
  running runtime cases.
- Focused apiref/apispec/docs tests, check-api-spec, sync-docs --check,
  commentcheck against main and sleepcheck against HEAD pass. Commentcheck
  judges 1,077 files. git diff --check passes.
- There are no Go references to either removed Touch method, the run heartbeat
  field or Doctor's removed finalizer/report field. Local Git identity overrides
  and core.worktree are absent.

Native PostgreSQL, HTTP/api.sock, actual pods, SDK processes, platform execution,
full legacy suites, independent review and release gates are not claimed. No
Sparkwing binary or pipeline was run, as required by the brief. The lead retains
review, pipelines, Git writes and landing. Public .apidiff snapshots remain for
W9's final regeneration. The scratch directory is deleted.

## Amendment and handoff audit

The existing completed implementations remain covered by the W1 selection.
This continuation adds no foreign contract shape and advances legacy retirement.

| Entries | W1 disposition and remaining integration |
| --- | --- |
| A1-07/11/13-18 | Store settings/seals, source identity, MaterializeOnly, failure reads, scoped cancellation, execution state, expansion replay and attempt ordinals remain implemented. W3/W9 still need source-attempt transport and the production execution projections. |
| A1-08/20-26/55 | Ordered claims, provisioning ancestry, authentication snapshots, binding/source adapters, secret preflight, enrollment and here reclaim remain implemented. W2 owns trusted evidence providers and production route registration. |
| A1-21 cancellation handoff | Separate cancellation reasons, session digests/current membership and sealed CSRF persistence remain implemented. C2 BindingState still lacks the reason field needed for ReadAuthentication projection. No foreign type was edited. |
| A1-27 | W1's lifecycle transactions remain joined to W1b billing helpers. C2's real calculator, W3's current owner-compute reporting and W1b spend policy/legacy accounting retirement remain open. |
| A1-28/29/31-35 | W1b retains billing settlement, records, uploads, blob authorization, retention and log persistence. No implementation in that split was changed. |
| A1-38 | Safe SyncLauncher remains blocked on W8/W9's immutable award generation and Kubernetes Job-instance correlation. Store UnitID alone cannot identify an award. |
| A1-40/44/45 | Store owner recovery and shared resolution remain implemented. W8 owns native local-controller readiness/lifecycle. |
| A1-51 and W1 scope handoff | Both raw Touch APIs, their callers, independent tickers and Doctor finalizer are now removed. StateStoreOld, raw lifecycle/readiness APIs, CreatePlanNode and remaining claim/trigger writers still need retirement. |
| A1-53 | Full native route, platform, backend and real-repository evidence remains with T1/lead. SQLite/package proofs do not close it. |

The final C2 POST /api/v1/runs/{run}/heartbeat policy remains declared. W2 must
bind it to the implemented EngineController.Renew adapter, including current
watched-job delivery. The retired legacy handler wrote a run timestamp and
accepted node holders. It cannot serve that final owner-lease contract. The
native host renewal-loss behavior remains a real-integration point. No new
architecture question or requested foreign contract change was introduced.

## Shared files

The lead's caller-migration exception authorizes the edits below. The changes
remove calls, contract members and fixtures tied to the retired APIs. The
Kubernetes and Doctor removals also delete their sole caller behavior.

- cmd/sparkwing/doctor_test.go
- cmd/sparkwing/runs_standalone_test.go
- internal/acceptance/store_owner_unstarted.go
- internal/nodehost/attempt_test.go
- internal/nodehost/closures_test.go
- internal/nodehost/expand_unix_test.go
- internal/nodehost/generated_test.go
- internal/nodehost/memo_unix_test.go
- internal/nodehost/plan_test.go
- internal/nodehost/podnode_test.go
- internal/nodehost/retry_eval_unix_test.go
- internal/nodehost/scheduler.go
- internal/nodehost/steps_test.go
- internal/opsview/daemon_blindness_test.go
- internal/opsview/doctor.go
- internal/opsview/doctor_plain_coverage_test.go
- internal/opsview/doctor_wedged_test.go
- internal/orchestrator/backends.go
- internal/orchestrator/envelope_coverage_test.go
- internal/orchestrator/hosted_retry.go
- internal/orchestrator/httpconcurrency_test.go
- internal/orchestrator/run_stores_test.go
- internal/orchestrator/wingd_api_contention_test.go
- internal/orchestrator/wingd_api_routes_test.go
- internal/orchestrator/wingd_api_test.go
- internal/runners/k8s/dead_token_test.go
- internal/runners/k8s/k8s.go
- pkg/controller/client/client.go
- pkg/controller/client/watch_test.go
- pkg/controller/handlers.go
- pkg/controller/log_segments_internal_test.go
- pkg/controller/node_claim_ownership_test.go
- pkg/controller/node_client_contract_test.go
- pkg/controller/runner_scopes_test.go
- pkg/controller/server.go
- pkg/controller/tenant_routes.go
- pkg/storage/state.go

Shared prose is c1-data.md, handoffs.md, t1-port-inventory.md, CHANGELOG.md,
docs/auth.md, docs/observability.md and docs/migrations/_unreleased.md. Generated
API reference and OpenAPI changes remove the heartbeat/touch surfaces and field.
Regeneration also corrected the previously stale legacy plan route scope and
claim-acceptance description. Embedded mirrors were generated with sync-docs.

## Friction

The first deletion pass also removed the job heartbeat scanner assignment.
Diff review caught it and it was restored before final checks. The companion
node cut's first build found a remaining dispatcher loop launch. It was removed
with the loop. Caller audit caught and corrected the contention sample labels.
The final tree passes the checks above.

The sandbox restrictions and swarm brief leave actual sockets, pods, PostgreSQL
and native platforms unverified. Contract fakes and real SQLite exercise the
store boundaries. No native result is inferred from them.

STATUS: CONTINUE
