# W8 award fence continuation

Bootstrap exchange now checks the complete source projection against the private
award used by fetch-source. Repository, remote, commit, pipeline directory,
default branch, bundle, checkout policy and engine must match before dispatch.
Claim publication, source initialization and exchange share private value capture.
The launcher no longer carries injectable source-projection callbacks. Both
claim and exchange call W3's actual projection functions.

Invariant: an exchange cannot dispatch source or execution identity different
from its private award. `FuzzBootstrapSourceIsImmutable` generates changes to
nine source fields across plan, eval and work. Its seeds reproduced execution
of altered source before the fix. Existing exchange fence, resource, engine,
cancellation and single-use checks remain in place.

Invariant: sync cleanup can select only the Job namespace, name and UID observed
before transport. `FuzzControllerSyncRetainsObservedIdentity` mutates the request
inside the transport and returns the changed cleanup identity. Its namespace
and UID seeds reproduced acceptance before the fix. Expected identities now
remain private to the adapter.

## Lead decisions 07:40

| Item | Disposition |
| --- | --- |
| 5 | `LocalController.HTTP() LocalHTTP` remains the sole handler interface. The runtime owns both servers, cancellation and joined drain. W2's production handler adapter remains unbound. |
| 6 | Runtime and merged registry agree: required digest-pinned `--docker-image`, no launcher `--deadline`, positive optional `--poll`. Existing parser/runtime acceptance passes. No registry edit was needed. |
| 10 / A1-38 | W8 sends kind, unit ID, holder and live claim generation, plus actual Job namespace/name/UID and node UID. Eval keeps its own lease generation separate from source generation. Responses correlate by the full fence; removal requires the observed instance and Kubernetes UID precondition. These W8 portions are implemented. **A1-38 is not closed end to end.** |

W2's `EngineController.SyncLauncher` delegates through `LauncherSyncReader` and
strips returned credentials. No production `ReadLauncherSync` implementation
exists in this tree. W1/W2 must bind the transactional ancestry and award fence
reader. `claimedUnit` also clears `Execution` for plan/eval. Those units cannot
launch or exchange until the controller supplies their resolved resources under
the existing wire contract. Component tests use these interfaces with fakes.

## Hard cuts and caller migration

The cluster worker, warm-pool dispatcher, run-host Kubernetes dispatcher and old
W8 Job builders remain deleted by prior commits. Their removed flags occur only
in refusal tests. This session removes the remaining empty runner Role and
RoleBinding template and the unused `rbac.create` setting. Ordinary runners call
the controller and need neither object. The optional launcher's separate RBAC
remains in its owning template.

Production line accounting against this session's starting HEAD, excluding
tests, Markdown and generated archives:

| Scope | Added | Removed | Net deleted |
| --- | ---: | ---: | ---: |
| Launcher Go | 47 | 30 | -17 |
| Chart YAML/templates | 3 | 39 | 36 |
| Total | 50 | 69 | **19** |

Deleted file: `charts/sparkwing-runner-bundle/templates/rbac.yaml` (26 lines).
The parent chart's stale RBAC pointer and child values are migrated. The existing
render test now refuses runner Roles and bindings. Helm regenerated the vendored
runner bundle, and its source comparison passes. The source README describes the
separate runner and launcher permissions.

Minimal shared test migrations:

- `tests/acceptance/bootstrap_w8_test.go` supplies a real sealed source location
  and commit matching the private award.
- `tests/acceptance/bootstrap_store_w8_test.go` derives source from the persisted
  accepted spec instead of inventing a different commit.
- `tests/acceptance/runner_identity_w8_test.go` gives its exchanged award the same
  accepted source used for execution identity.

No foreign production Go caller changes. No archived test port or new native
acceptance cell is claimed. Existing W8 component cells remain passing.

## Amendment and handoff coverage

All 26 amendment rows naming W8 and the W8 handoffs were checked against the
committed implementation and this continuation. Earlier completed mechanisms
were retained.

| Entries | Disposition and real integration left open |
| --- | --- |
| A1-08,09,26 | One ordered claim loop, explicit launcher transport and server-derived identity remain implemented. Final enrollment/profile provisioning and default process bindings remain open. |
| A1-10,11,12 | Exact selected-engine installation and shared source/environment contracts remain implemented. Exchange now checks the same source projection as initialization. Production source-access transport and initialized-checkout consumption remain open. |
| A1-18,27 | W8 retains initial/resumed attempt ordinals and distinct claim generations. Existing lifecycle/store components pass. Billing transaction completion belongs to W1/W2. |
| A1-19,36,39 | Awarded runner/engine, sealed run and job settings, shared leases and owned-store native hosts remain implemented. Remote NativeHostStore and process/log/storage service construction remain open. No legacy dispatcher is restored. |
| A1-20 | W8 consumes the approved provisioned claimant interface. W1/W2 own seeded bootstrap-admin issuance. No alternate issuer is added. |
| A1-38 | Launcher fences, instance correlation, private transport and exchange source checks are implemented. Transactional sync and plan/eval resource projection remain open as described above. |
| A1-40,41 | Owned store, queue, reaper, admission, four holds, on-demand lifecycle, managed units and joined drain remain implemented. The production State/HTTP/claimant/native-service binder and CLI resolver remain open. |
| A1-42,53 | Deployment Probe/Clear, enforced network isolation, aggregate scratch, teardown, native platform evidence and real-repository QA remain open. Native cells stay blocked. |
| A1-43 | Signed historical matrices, exact pin resolution and policy enforcement remain implemented. Notice rendering and release/native evidence retain their owners. |
| A1-45,46,48 | Shared settings, environment, proxy masking, captured snapshots and deadlines remain consumed at W8 boundaries. Final production host bindings remain open. |
| A1-49,50,55 | Private credential enrollment/cleanup, named here claim/resume and runtime ports remain implemented. Production local claimant/services and final CLI host/cache/publish composition remain open. |
| A1-51,52 | W8 uses the v2 host and private award contracts. SDK parity, generated references, packaged Windows bridge and native inheritance evidence retain W4/W5/W9/lead ownership. |
| Pending-claim DESIGN entries | The committed outcome and arbitrary-cancellation classification remain in place. Their original regressions pass with the runners suite. |

## Contract changes needed and remaining W8 work

No foreign interface changed in this continuation. W1/W2 owe implementations of
their existing sync and local-controller service contracts. W9's existing
source client still returns the legacy store credential type rather than
`SourceCredentialResponse`. The final source-access binding needs that shared
transport, not another W8 HTTP decoder.

W3 must supply a native plan/eval unit entrypoint contract. `OwnerHost.Run` owns
the complete trigger-hosted lifecycle and is not a plan-only executor for a
controller-owned run. `NewNativeHost` supplies owner, claimed-node and owner-eval
hosts, but does not declare a Cloud plan/eval award lifecycle. W8 does not change
W3's contract or run controller-owned scheduling inside a planning pod.

W8 still owes the default executable Runtime bindings, native remote hosting,
initialized-checkout revalidation/installation, final local-controller binder,
and deployed Probe/Clear implementations. `NativeHostStore` and service-binding
interfaces are available and the existing owned-store components use them with
fakes. Real deployment, native login/update/transport and platform matrices
require the lead's infrastructure and native drivers.

## Assumptions and mechanisms

Private value capture reuses the award's existing JSON transport because shallow
copies retain mutable source policy and engine pointers. Undo removes the common
capture helper and restores the three callers. `cloudSource` shares the existing
projection and accepted-engine check between claim and exchange. The smaller
binding-only check admitted changed source. Undo restores the two prior bodies.
Sync retains expected Job values rather than trusting its request after transport.
No exported API, credential issuer, setting, timeout or environment variable is added.

## Verification

Checks use `GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOMAXPROCS=4`, `-p 4`, and
GOCACHE/GOTMPDIR/TMPDIR under `/tmp/sw-astra-W8`.

| Check | Result |
| --- | --- |
| Whole-repository `go build -p 4 ./...` | Passed. |
| Vet: runners/..., engine/..., localcontroller, cluster, charts | Passed. |
| Tests: runners/..., engine/..., localcontroller, internal/acceptance, clisurface, charts | Passed. |
| Cluster tests | Passed with `TestStartMetricsListener_ServesMetrics` excluded because it binds a socket. |
| `tests/acceptance -run 'W8\|LauncherCloud\|LauncherController\|LauncherPolling'` | Passed. Existing component and SQLite/store evidence only. |
| Each new property, 1,000 requested fuzz executions | Passed. Source ran 1,001 executions; sync ran 1,000. |
| Vendored chart source comparison after regeneration | Passed. |
| `go run ./internal/commentcheck -base main .` | Clean across 2,124 Go files. |
| `git diff --check` | Passed. |

Independent review, native/deployed acceptance and pipelines remain lead-owned
under the swarm brief. No release, deployment or repository Git write ran.
The initial orientation ran the installed `sparkwing info --for-agent` before
reading the swarm brief's binary prohibition. No later Sparkwing binary ran.
Overwing reports its installed binary ten commits behind source. No local Git
identity or core.worktree override was present. All check sessions completed,
and `/tmp/sw-astra-W8` was removed after the fuzz processes joined.

STATUS: CONTINUE
