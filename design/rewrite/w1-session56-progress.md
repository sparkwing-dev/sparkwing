# W1 session 56: job insertion, readiness and sealed reporting cuts

W1 remains CONTINUE. The starting and final HEAD is d86ae4e0e. No Git writes
ran. Lead-owned handoffs and amendment files are unchanged. W1b production,
materialization, billing settlement and blob/log persistence are unchanged.

## Implemented and deleted

Removed `Store.CreateNode`, `Store.MarkNodeReady` and
`Tenant.MarkNodeReady`. `insertNodeTx` now applies job-count guards for both
accepted plans and generated expansions. It runs under the acceptance owner's
run lock, so concurrent acceptance cannot exceed the configured per-run cap.
Refusal rolls back the seal, every inserted job and the expansion barrier.
Exact plan/expansion replay stays valid after the configured cap falls.

The metered guard reads committed owner/plan award billing facts in capabilities,
scoped to the tenant and run. It no longer queries the removed tokens table.
Configured refusal preserves `ErrComputeLimit` and carries canonical
`plan_invalid`, including the cap and observed count in its message. The new
real shared-validation test reproduced the missing canonical error before its
fix and passes afterward.

Queue publication uses the owner's `TransitionNode` with `NodeReady` and
`CauseRelease`. Replay preserves the first queue timestamp and state sequence.
Removed the readiness-only CPU-class helper, unused run-to-tenant fallback,
dead lifecycle predicates and `Trigger.IsFinished`.

Removed the legacy invocation redactor, `Run.RedactedForDisplay`,
`Run.SecretArgNames`, `RedactInvocation`, `RedactedRun`, `RedactedRuns`,
`RedactedArgValue` and `IncludeSecretValues`. HTTP and dashboard callers consume
redacted reporting views from the existing C4 display projection. Receipts use
that same view and omit input hashes for unknown/invalid classification and
supplied secret args. Explicit empty classification keeps public arguments and
their hash. The generated store reporting property now also checks receipt
output. The raw run-insertion validator stays until its caller can retire.

GitHub commit reports use one tenant-scoped query over sealed runs and the
shared trigger scanner. They select canonical numeric repository identity and
exact SHA, preserving runs across renames while refusing other IDs and teams.
The duplicate ID scan and per-result read loop are removed.

Removed the unused `child_invocations` table and its schema inventory entry.
Child admission already stores lineage and invocation replay on runs. Its
existing lineage, replay and live-parent properties still pass. The tenant
writer guard now reports three missing writers instead of four.

## Production volume

W1 store production against HEAD: **75 added, 405 deleted, net -330 Go lines**.
Combined W1+W1b against main: **19,159 added, 23,321 deleted, net -4,162**.
The -4,250 target remains **88 lines away**. Counts exclude tests, acceptance,
testdata and contract declaration files, matching the lead's script.
No budget waiver or COMPLETE claim is made. Remaining raw run/trigger insertion
is larger than the remaining gap and still awaits the trusted-origin contracts.

Fixture-only `SeedNode` and `SeedNodeReady` replace dependencies on the deleted
writers. They do not implement admission or grant authority. A current-schema
fixture test verifies timestamps, replay and missing-target errors. The lead's
production count includes these helpers. No production interface, configuration
option, service or caller-selected clock was added.

## Verification and ports

Commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache and
TMPDIR/GOTMPDIR under /tmp/sw-astra-W1. Tests use -p 4.

- Whole-repository `go build -p 4 ./...` and scoped vet pass.
- The 21 established W1 acceptance functions and priority placement/metering,
  ordered-preference, rotation, SQL-scope and schema checks pass in 12.544s.
- Insertion, expansion rollback, concurrent acceptance, readiness replay,
  reporting/receipt, classification, GitHub identity and fixture checks pass.
- `TestStorePlanInsertionLimitUsesCanonicalRefusal` uses real intake, claim,
  shared validation and sealing. It checks rollback and lowered-cap replay.
- All controller `TestEngine` checks pass in 5.935s.
- Scoped race checks across store, teststore, web and receipts pass. The store
  portion took 62.991s. Final canonical-refusal and concurrent-insertion race
  checks pass in 3.317s after the canonical error change.
- Commentcheck passes across 2,359 files. Changelog, whitespace and embedded-doc
  checks pass. Changelog checking prints its existing rename-detection warning.
- Both store API snapshots are regenerated. The store snapshot also captures
  the imported `BlobManifestReader.VerifyBlobManifest` declaration. Three
  imported snapshots still drift: pkg_controller, pkg_storage and pkg_storage_s3.
  Their owners/lead must regenerate them. A clean whole-repo API gate is not claimed.

The port inventory records replacements for the old node-creation guard tests,
concurrent creation, ready idempotency, GitHub check/commit reporting and legacy
redactor tests. New checks are in node_insertion_limits_test.go,
github_commit_reporting_test.go, reporting_seals_test.go, the plan-seal acceptance
file and teststore/run_test.go. Raw run-rate assertions remain until raw intake
retires. The broader legacy fixture suite was not run or represented as green.
No whole-repository tests, PostgreSQL/native matrix, pipelines, release or
independent review are claimed. The lead owns those checks.

## Contract changes needed and remaining work

Priority Cloud metering, ordered placement preferences, rotation grace, launcher
fencing, harness moves and dead contract-table cuts were completed in prior
sessions. Their current properties pass. Materialization remains closed.

Trusted schedule/App admission remains open. C4 needs the tagged committing
origin through IntakeFacts/SealedRequest. C2 needs a verified-delivery fact reader
usable inside the committing store transaction. Current IntakeRequest still
requires a credential authority before its policy is called. An arming account
or verified delivery is not that credential. Re-read schedule/version or
installation/consent facts at commit, then route both origins through admitRun
and delete triggerIntake, triggerRows and legacy run/trigger insertion. No
synthetic credential or foreign contract shape was introduced.

The three failing writer findings remain explicit: `debug_pauses` needs its
new authority-bearing creation transaction. `storage_reservations` and
`storage_commit_receipts` have no writers and belong to W1b's legacy storage
retirement. No guard exemption hides them.

Other open integrations from the prior report remain: concrete C2 TeamWriter,
GitHubAppWriter and run-control adapters, full runner inventory/capacity and
renewal reporting, sealed GitHub completion facts instead of TriggerEnv, publisher
metric binding and usage exact-body identity. Raw CreateRun/CreateTrigger paths,
run-rate token queries and their legacy fixtures still need the final cut.
The accounting and blob/log portions of amendments remain W1b's ownership.

## Shared files and friction

Production caller migrations outside store:
- internal/orchestrator/receipt/receipt.go
- internal/web/reads.go
- internal/web/runs.go
- pkg/controller/handlers.go
- pkg/controller/retry.go

Shared fixture migrations outside store:
- internal/backend/store_backend_test.go
- internal/orchestrator/receipt/receipt_test.go
- internal/web/secret_args_display_test.go
- pkg/controller/compute_limits_test.go
- pkg/controller/concurrency_team_test.go
- pkg/controller/credits_test.go
- pkg/controller/github_checks_test.go
- pkg/controller/github_runners_test.go
- pkg/controller/node_claim_fixture_test.go
- pkg/controller/profiles_aggregation_test.go
- pkg/controller/profiles_coverage_test.go
- pkg/controller/profiles_test.go
- pkg/controller/prom_internal_test.go
- pkg/controller/secret_args_display_test.go
- pkg/controller/source_credential_test.go
- pkg/controller/team_boundary_test.go

Minimal W1b fixture migrations within store only change calls in
credit_rates_test.go, credits_per_team_test.go, credits_test.go and storage_test.go.
They use the explicit fixture seed helpers. No W1b production file was edited.

C1, the port inventory, migration source, changelog, embedded copies and two store
API snapshots are updated. handoffs.md and amendments-*.md are unchanged.

Friction: the previous reporting API left a duplicate invocation redaction layer.
Removing it exposed the now-unused classification getter, which is also deleted.
The old node writer's guard still queried deleted tokens and was absent from real
acceptance. Its one insertion owner now reads committed award facts instead.
The trusted-origin contract gap still prevents the run/trigger cut. Git writes
and native/socket checks remain outside the worker's allowed workflow.
Scratch is removed at the end. The shared build cache remains.

STATUS: CONTINUE
