# W9 public retry history continuation

Base HEAD: `a3a27dc4a`. The lead's native local-controller, local-schedule and
public-doctor passes remain the starting evidence. The SDK replacement pin
policy and lead-owned handoffs and amendments are unchanged.

## Implemented and deleted

The merged store already reads retry history from sealed request/spec columns.
The previous report's missing `args_json` reader is therefore superseded.
The public attempts route still returned legacy `store.Run` rows, including
`invocation`, and checked only the selected run's binding. A run-scoped owner
could read sibling executions outside its award. Both failures ran red through
the real HTTP router and SQLite.

The route now returns the existing shared `RunPage` and `RunDetail` projections.
It rechecks every member's binding and buffers the response until all members
pass. Redacted inputs, current execution lineage, jobs, attempts and engine
notices use the same projection as run status. No raw reporting row, invocation
metadata, source path, environment digest or private seal enters the response.
The invariant is that every returned run belongs to the authenticated team and
binding and crosses only the shared public display boundary.

The dashboard consumer reads `inputs.lineage` and uses a typed public history
entry. Its legacy `invocation.flags` badge logic is deleted because the store's
reporting reader never supplies those flags. No reachable retry-mode badge is
removed. C3, OpenAPI, the API guide, migration guide, changelog and embedded
copies describe the response cut. No new route, grant, schema, environment
variable or API signature was added.

Independent review found an inherited 256-link traversal limit. The new HTTP
regression reproduced a 274-run history beginning at run number 19 when selected
by its leaf. The store now detects visited identities instead of imposing that
limit. Root, leaf and numeric selectors return all 274 members, including with
a cycle back to the leaf. Equal creation times sort by team-local run number.
The nondeterministic timestamp order also ran red before its fix. This is a
minimal store implementation change with no foreign contract change.

Deleted production logic is **7 Go lines and 46 TypeScript/TSX lines**.
Production Go this session is **+26/-7, net +19**. The frontend change is
**+16/-46, net -30**. No whole production file was deleted. W9-owned production
Go versus main remains **+16,886/-23,807, net -6,921**, within the -2,900 budget.
Tests, testdata, acceptance and contract declarations are excluded from counts.

## Ports and evidence

`TestRetry_ListAttempts` is replaced by
`TestRetryHistoryUsesPublicProjectionForEverySelector`. The old socket fixture
is deleted, removing 47 test lines. New persisted regressions cover per-member
binding, long chains, cycles and timestamp ties. The generated-input property
varies private values, selectors, revocation, membership loss, owner credentials
and cross-team lineage pointers. No native acceptance cell is newly enabled or
claimed complete. The existing surface and engine-amendment selections pass.

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository build and focused controller/store/apispec vet.
- Retry-history, numeric selector and debug-selector controller selections.
- Focused sealed-reporting and retry/team store tests.
- Full short apispec, apiref, doccheck, controllerwire and controller/client suites.
- Retry-history property fuzzing, 369 executions, and the complete retry-history
  selection under race, including long chains, cycles and property seeds.
- TypeScript checking without emitting or incremental state and lint of both
  frontend consumer files.
- Surface/engine-amendment acceptance, embedded-doc, privacy and symbol checks.
- Generated API/spec checks, mirror drift, whitespace and commentcheck against main.

The expanded race selection passed in 54.8 seconds. Independent review found no
introduced blocker and accepted the follow-up traversal and ordering fix.
Native browser/service-manager
and platform QA remain with the lead. No full-repository test suite, pipeline,
candidate install, operational Sparkwing command or Git write ran.

## Remaining contracts and integrations

- **DESIGN: owner/eval metadata authority.** C1 `checkResultWriterTx` still
  requires a live node holder and positive attempt. The tagged
  `TestReviewOwnerEvaluationReportsBeforeWorkAward` remains the required red
  regression. C1/C2 must carry the live owner/eval source-job fence into the
  committing write. This remains the previous two-strikes design handoff.
- **C2 owner-source relation and eval services.**
  `bindingPipelineReferenceTx` still requires UnitNode. PurposeEval's fixed grant
  table remains ExpansionSubmit/DataRead, while masks and cleanup need
  NodeReport. No node authority or wider grant was invented.
- **C1/C2 annotation locations.** Column and end_line reach W9's transport but
  remain absent from the store/controller write declarations.
- **W8 launcher boundary.** ProductionRuntime still cannot bind Launcher because
  KubernetesBoundary.Probe and Clear have no production implementations. The
  shipped-runner done test remains open for launch. Start, warm, fetch-source
  and run-node retain their prior production-HTTP evidence. An error-only
  factory would not complete this integration.
- Canonical remote placement and here hosting, remote-cache source/read-set
  proof, publication, Cloud plan/eval/bootstrap source consumption, retained
  scheduled intake, settled receipt facts and Kubernetes debug targets remain
  at the boundaries in W9-REPORTING-CONTINUATION.md,
  W9-DATA-TRANSPORT-CONTINUATION.md and W9-MERGE-RECEIPT-CONTINUATION.md.
  The retry-history boundary in those reports is now closed.

W8 findings 6-9 and peer intake retain their completed local implementation.
The private JSONL writer remains deleted. Complete owner/eval closure execution
and native local execution still require the contracts and SDK policy above.

Shared files touched: `internal/controller/retry.go`, `retry_test.go`, the new
`retry_history_native_test.go`, `internal/store/tenant_runs.go`,
`internal/apispec/wire.go`, `web/src/components/AttemptsDropdown.tsx`,
`web/src/lib/api.ts`, `api/openapi.yaml`, the port inventory and generated
`pkg/docs` copies. The frontend files are the only production caller migrations.

Mechanisms added: none. Existing projections and binding checks own the response.
The traversal's visited set replaces its fixed bound. Friction remains foreign
contracts, sandbox sockets and native service managers, and Overwing's reported
installed-binary lag. Scratch is removed after all checks join. The shared build
cache is retained.

STATUS: CONTINUE
