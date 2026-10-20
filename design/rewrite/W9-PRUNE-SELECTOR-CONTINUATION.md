# W9 prune and selector continuation

Base HEAD: `19ecec054`. The lead's native local-controller, local-schedule and
public-doctor passes remain the starting evidence. The SDK replacement pin
policy and lead-owned handoffs/amendments are unchanged.

## Implemented and deleted

The real controller deleted a run successfully, then the shared client returned
an error because it expected a JSON receipt after HTTP 204. Both stable-ID and
numeric-selector regressions reproduced this against SQLite without a listener.
`RunPruner.DeleteRun` now returns only an error and requires the server's empty
204 acknowledgement. The CLI emits the selected run and any structured failure.
It no longer invents deleted-row or replay facts. The unsupported RunDeletion
type and result rendering are deleted. This is a local C3 contract correction.
No C1 or C2 contract, grant or transaction changed. The prior request for an
atomic deletion-result projection is unnecessary for this acknowledgement API.
Absent-run selector resolution and cleanup replay still belong to C2.

The CLI collector also changed every numeric selector and unprefixed stable ID
before cancel, retry or prune. For example, `42` became `run-42`. These are
different selectors and could name different runs. The prefix helper is deleted.
Flags and stdin now preserve selectors, trim surrounding whitespace and dedupe
only exact matches. The authenticated controller resolves identity. Tests keep
`42` and `run-42` distinct through all three action paths and the shared HTTP
client. The persisted prune regression also keeps a separate `run-1` row while
deleting run number `1`.

Invariants: a run action cannot fabricate another run's selector before
authorization, and prune success requires the complete server acknowledgement.
Generated-input tests exercise flags/stdin and adversarial status/body pairs.
The CLI and API docs, migration guide, changelog, OpenAPI and embedded mirrors
match the cuts. The step-window paragraph formerly appended to generated CLI
docs now lives in the command registry, so regeneration preserves it.

No whole production file was deleted. Production Go this session is
**+22/-43, net -21**. W9-owned production versus main is
**+16,886/-23,807, net -6,921**, within the -2,900 budget. Counts exclude tests,
testdata, acceptance and contract declaration files.

## Contract changes and real integrations still needed

- **DESIGN: owner/eval downstream metadata authority.** The existing tagged
  TestReviewOwnerEvaluationReportsBeforeWorkAward still names the required
  behavior. C1 checkResultWriterTx requires a positive node attempt even after
  C2 authorizes owner evaluation. Evaluation must retain its live owner/eval
  fence and source-job relation through the committing write. The previous
  report's two-strikes design handoff remains open. No synthetic attempt or
  wider holder right was added.
- **C2/W3 owner-source authority.** Persisted latest-reference authorization
  still requires UnitNode in bindingPipelineReferenceTx. UnitEval also lacks
  NodeReport for its mask/cleanup service routes. Existing owner/eval process
  credentials do not close these downstream relations. These require C2
  contracts rather than borrowing a work credential.
- **C1/C2 annotation locations.** Column and end_line reach W9's transport, but
  their store and controller request declarations still omit them.
- **W8 production launch boundary.** ProductionRuntime still lacks Launcher
  because KubernetesBoundary.Probe and Clear have no production implementations.
  Supply enforcement evidence and reservation-scoped cleanup through the existing
  CloudBoundary interface. Start, warm, fetch-source and run-node retain their
  prior production-HTTP evidence. The shipped-runner launch regression remains
  red. No placeholder factory was added to change its error message.
- The canonical placement decision, remote-cache source/read-set proof,
  publication, structured log pages, Cloud plan/eval and bootstrap checkout
  consumption, retained trigger environment, here reason, scheduled sealed
  intake, retry history, settled receipt facts and Kubernetes debug targets
  remain at the foreign boundaries recorded in W9-REPORTING-CONTINUATION.md,
  W9-DATA-TRANSPORT-CONTINUATION.md and W9-MERGE-RECEIPT-CONTINUATION.md.

The merged implementations for W8 findings 6-9 and peer intake remain in place.
This session does not repeat completed lifecycle, storage/log, peer-intake or
process-credential work. The private JSONL writer remains deleted. No native
acceptance cell is newly claimed complete, and no archived test is newly ported.
The surface/query/prune transport acceptance and engine amendment selections pass.

## Evidence and scope

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository build and focused vet for all changed production packages
  and the controller integration boundary.
- Full short controller/client, controllerwire, clisurface, apispec, apiref and
  doccheck suites.
- Persisted prune, numeric/debug selectors, annotations and native process
  metadata tests. CLI cancel/retry/prune and selector tests pass.
- Changed CLI/client tests and persisted prune under race. Selector fuzzing
  completed 2,909 executions. Acknowledgement fuzzing completed 4,375.
- Surface/prune transport and engine amendment acceptance selections.
- Embedded-doc, privacy, symbol and changelog selections, generated API/spec
  checks, mirror/changelog drift checks and diff whitespace checks.
- Commentcheck against main.

Initial test fixtures used invalid run-state columns/values and omitted the
copied terminal timestamp. Those fixture errors were corrected. The corrected
prune regression ran red on the production acknowledgement mismatch before its
fix. The selector properties also ran red before removing the prefix helper.
An overly broad controller selection included a legacy socket-based retry test
and failed on `socket: operation not permitted`. The final focused selection
uses the socket-free native tests and passes. The acceptance fixture was migrated
after its old two-result DeleteRun call failed compilation.

Shared files touched: internal/apispec/wire.go,
internal/controller/prune_client_native_test.go,
tests/acceptance/surface_contract_test.go, api/openapi.yaml and generated pkg/docs
copies. API callers migrated: cmd/sparkwing/runs_prune.go, its fake,
internal/controller/client/prune_test.go and the acceptance transport fixture.
No caller in another production package needed migration.

Mechanisms added: none. No Git write, operational Sparkwing command, pipeline,
candidate install, deployment or release ran. CLI docs were regenerated through
the existing renderer in a temporary test, which was removed. Native service,
placement and platform verification remain with the lead. Git has no local
identity or core.worktree override. Worker scratch is removed after checks join.
The shared build cache is retained. Friction remains sandbox sockets, foreign
contracts and Overwing's reported installed-binary source lag.

STATUS: CONTINUE
