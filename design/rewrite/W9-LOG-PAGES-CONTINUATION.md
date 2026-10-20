# W9 log-page continuation

Base: `6f5c0d88c`. The readiness, public doctor and daemon-cleanup fixes from
the preceding sessions remain implemented. Their focused regressions pass.
Native command reruns remain with the lead. This continuation connects the
single-run typed log reader to persisted storage.

## Implemented

The run, job and run-search log endpoints return C3 `LogPage` responses when
the caller requests JSON. The dashboard's NDJSON and text representations keep
their existing handlers. Subjects come from `ReadRunView`, bytes from
`logseg.CommittedReader`, and events from the shared public event projection.
The controller meters the resulting JSON response through the existing log
egress path.

The server retains each subject's line position and each run's event sequence
in an opaque cursor. The selection hash binds the team, run and filters. Since
retains its initial cutoff. Head, tail, line ranges, grep, event selection and
descendant traversal share that cursor. Pages carry at most 500 records and
continue without dropping remaining records. This is pagination, not a log
retention limit. Completion requires persisted terminal state and sealed
selected streams. Missing committed bytes and incomplete streams fail loudly.

The security invariant is that a cursor never replaces current authorization
of its exact persisted subject. Reads still pass through route authorization
and the store's segment authorization, including when the cursor has already
consumed every line. Descendants pass through the store binding check before
their run view is read. Cursors contain positions and canonical identities,
not log payloads or credentials.

## Evidence

The initial regression failed against the existing endpoint with
`typed log reader received raw bytes: "first\\nsecond\\n"`. The final test uses
the shipped `client.NewLogReader`, policy router, persisted node authority,
SQLite and filesystem objects. Only HTTP socket delivery is replaced by an
in-memory round trip. It verifies canonical attempt identity, line numbers,
non-repeating idle polls and nonterminal completeness.

The new tests also cover explicit zero tail, head, ranges, timestamp filtering,
multi-page continuation, malformed selections, changed cursor scope, revoked
authority, owner-unit identity and missing committed bytes. A later-subject tail
regression also failed before tail initialization moved to each subject's first
visit. Fuzz seeds generate foreign team, run,
job, generation and attempt selectors with forged consumed positions. The new
tests pass under the race detector.

The bounded authority fuzz completed five executions without a failure.
`TestNativeStoreOwnerW8` passes all modes, and
`TestEmbeddedOwnerSealsLogsDuringCancelledHostCleanup` passes.

Whole-repository `go build ./...` and focused vet pass. Focused controller
doctor, peer-intake, shared-storage and log-reader tests pass. The lifecycle,
service-installation and readiness suites pass. Full short suites pass for
localcontroller, controller/client and cluster. CLI doctor, detached-entrypoint,
queue-construction and production-controller tests pass. The CLI and runner
`rewrite_integration` suites compile. Doccheck, apispec, commentcheck and the
embedded-document drift check pass.

No archived test was newly ported. No native acceptance cell is newly claimed.
No socket, daemon, candidate install, operational Sparkwing command, pipeline,
Git write or release ran.

## Contract changes needed and remaining integration

No foreign contract changed. The earlier requests in
[W9-HANDOFF.md](W9-HANDOFF.md) and
[W9-READINESS-CONTINUATION.md](W9-READINESS-CONTINUATION.md) still apply, except
that the single-run structured-log transport now has a real server binding.

- **C2/W3 owner/eval credentials.** `CredentialNodeProcess`,
  `ProcessAuthorities.Delegate` and `NativeProcesses.Delegate` require a node
  attempt. The separate `NativeConfig.Evaluate` contract still has no production
  authority adapter. Owner/eval lease and source-job identity must survive
  independently of a node award. The existing fake binder test remains boundary
  evidence. No attempt is fabricated here.
- **W8 Cloud boundary.** `KubernetesBoundary.Probe` and `Clear` have no production
  implementation. They must establish network enforcement and reservation-scoped
  teardown. `ProductionRuntime.Launcher` remains unbound. The installed-runner
  launch probe remains open. Start, warm, fetch-source and run-node retain their
  production factories from the previous sessions.
- **Cross-run search.** `/runs/grep` still needs C3 `LogSearchPage` composition
  with final `RunQuery` filtering and candidate-run pagination. This continuation
  does not claim it works through the typed client.
- Canonical remote placement eligibility, cross-run record/read-set proofs,
  bootstrap checkout consumption, scheduled sealed intake, here/cache/publication
  and native platform execution remain as recorded in the preceding reports.
  Descendant-log integration still needs a native lifecycle test.

The lead should rerun `TestReviewProductionExecutionBindings`,
`TestReviewPublicDoctorSweepsAndWarnsForPrunedRun` and
`TestLocalClaimantW8UsesPersistedPeerAuthority` natively with isolated state.

## Scope, size and assumptions

Shared production edits are `internal/controller/log_pages.go` and the four-line
registration change in `internal/controller/reporting_routes.go`. The new test file
is `internal/controller/log_pages_test.go`. Documentation changes are
`docs/local-execution.md` and its generated mirror. Lead-owned contracts,
handoffs and amendments are unchanged. No API was removed or caller migrated.

This continuation adds 292 production lines and deletes zero. No whole legacy
file was deleted. W9-owned production paths versus main remain
+16,876 / -23,797, net **-6,921**, within the -2,900 budget. The 292 lines above
are shared controller integration and are stated separately from that path count.

The added cursor and JSON projection implement the existing C3 contract.
A client adapter over rendered bytes cannot recover canonical subjects, seal
state or event sequences. Removing `log_pages.go` and its registration reverts
this continuation. No option, environment variable, interface, retry or timeout
was added. The raw dashboard representations remain because the dashboard
explicitly requests NDJSON and uses the existing search response.

Friction remains the socket restriction and the foreign owner/eval and Cloud
boundary contracts. Scratch is removed after checks join. `/tmp/sw-gocache` is
retained. Independent landing review remains with the lead.

STATUS: CONTINUE
