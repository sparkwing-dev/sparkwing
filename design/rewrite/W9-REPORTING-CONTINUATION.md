# W9 reporting continuation

Base HEAD: `3d214da40`. The lead's native local-controller, schedule and public
doctor passes remain the starting evidence. The SDK replacement pin policy is
unchanged. Lead-owned handoffs and amendments were read and left unchanged.

## Implemented and deleted

The shared annotation client now accepts the controller's empty HTTP 204
acknowledgement. The persisted regression reproduced a committed annotation
followed by a client error because the client expected an Applied JSON result.
RunReview.AppendAnnotation now returns only an error. Its CLI caller and fake
are migrated, and JSON output no longer invents transition or replay facts.
C3, OpenAPI, the migration guide, changelog and embedded mirrors match this cut.
This changes only W9's contract. No foreign interface or grant changed.

The native process annotation adapter uses that shared writer. Its private DTO
and generic report helper are deleted. A regression reproduced the loss of
column and end_line. Both now reach the transport unchanged, including step
attribution. Empty masked text remains valid. The server still needs the C1/C2
location-field work below. It refuses unsupported fields instead of silently
losing them. Persisted job and step tests cover the supported location fields
and both annotation and summary writes through the real native process client.

The four job/step annotation and summary routes now resolve numeric selectors
through the existing authenticated team-local resolver. The regression was red
for run number 1 before this change and passes after it. Private state and
attempt transitions retain stable IDs.

The invariant is that metadata selection cannot change the current credential's
team, awarded job or live holder generation. The new property exercises all four
routes against SQLite, with stable/numeric, foreign and malformed selectors,
revocation and generation replacement. Refused requests leave annotations and
summaries unchanged. No route, grant, schema, limit or environment variable was
added.

## Remaining contracts and real integrations

1. **DESIGN: owner/eval downstream metadata authority.** Evaluation must report
   under its live owner/eval fence without acquiring a work attempt. The new
   TestReviewOwnerEvaluationReportsBeforeWorkAward is retained behind
   rewrite_integration. Both annotation and summary reach the real controller
   and SQLite, pass the owner route policy, then return HTTP 409 lock_held with
   reason=holder_rights. C1 checkResultWriterTx only accepts a current node
   holder and positive attempt. W1/W2 must define and commit the owner/eval
   source-job/generation relation independently of that attempt. C1's current
   AnnotationWrite/SummaryWrite do not carry that source fence. Do not fix this
   by creating a synthetic attempt or weakening holder checks. This is another
   downstream gap in the owner/eval mechanism, so the swarm's two-strikes rule
   requires a design handoff instead of another authority patch.
2. **C1/C2 annotation locations.** C1 AnnotationWrite/storedAnnotation and C2's
   annotationRequest omit column and end_line. W9's shared public type and native
   transport preserve both. Their persistence needs a foreign contract change.
3. **W8 launch.** TestInstalledRunnerRuntimeComposition still fails for launch
   with launcher runtime is unavailable. Start, warm, fetch-source and run-node
   reach their expected production HTTP operations. KubernetesBoundary.Probe
   and Clear still have only test implementations. W8 must provide enforced
   network evidence and reservation-scoped cleanup through CloudBoundary before
   ProductionRuntime.Launcher can be composed. No placeholder factory was added.
4. **Other owner/eval seams remain.** Persisted latest-reference authorization
   still requires UnitNode. UnitEval lacks NodeReport for masks/cleanup in the
   fixed grant table. Those need C2 changes rather than borrowed node authority.
5. **Existing remaining work** is recorded in W9-DATA-TRANSPORT-CONTINUATION.md
   and W9-MERGE-RECEIPT-CONTINUATION.md: canonical remote placement and here
   hosting, cache source/read-set proof, publication, Cloud plan/eval hosting,
   bootstrap checkout consumption, scheduled sealed intake, deletion receipts,
   retry-history reads, settled receipt facts and Kubernetes attachment targets.
   Their specific boundaries remain in those reports. Native local
   execution still waits on Korey's SDK replacement decision.

The new owner-metadata regression can be reproduced without a listener:

```text
go test -p 4 -tags rewrite_integration ./pkg/controller -run '^TestReviewOwnerEvaluationReportsBeforeWorkAward$' -count=1
```

Its two failures are recorded as failures. No native acceptance cell or archived
test port is newly claimed complete. Existing W8 findings 6-9 and peer intake
retain their merged implementations. Complete closure evaluation remains open
at the downstream contracts above.

## Evidence

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package workers.

Passed:

- Whole-repository build and focused vet, including the final route changes.
- Full short controller/client, controllerwire, apispec and apiref suites.
- Persisted annotation acknowledgement and native job/step metadata tests.
- CLI annotation, protocol acknowledgement and native source-location tests.
- Numeric metadata, existing numeric/debug, peer-intake and storage/log tests.
- Engine annotation, summary, reporting and surface/engine acceptance selections.
- New metadata authority property, persisted regressions and client/CLI tests
  under race. Metadata-selector fuzzing completed 128 executions.
- Full doccheck, generated API/spec checks, embedded mirror/changelog checks,
  documentation privacy/symbol selections, changelog and diff whitespace checks.
- Commentcheck against main.

The first numeric-metadata property attempt omitted Content-Type in its fixture
and failed at request decoding. The corrected request fixture passes. The new
production regressions were also run red before their fixes. No full-repository
test suite, operational Sparkwing command, pipeline, candidate install, release
or Git write ran. Native service-manager and platform QA remain with the lead.

## Scope and size

Production Go this session: **+28/-42, net -14**. No whole production file was
deleted. W9-owned production versus main: **+16,896/-23,796, net -6,900**, within
the -2,900 budget. Counts exclude tests, testdata and contract declaration files.

Shared files touched: internal/apispec/wire.go, pkg/controller/engine_routes.go,
pkg/controller/annotation_client_native_test.go,
pkg/controller/owner_metadata_integration_test.go, api/openapi.yaml and generated
pkg/docs copies. All other source/interface/caller changes are W9-owned. The
only caller migrations are cmd/sparkwing/annotations.go, its fake, the shared
client tests and NativeProcessClient.Annotation. No other area's caller needed
migration.

Mechanisms added: none. The existing authenticated transport and selector
resolver own the new paths. Independent landing review remains with the lead.
Friction is the foreign contracts above, sandbox native sockets and service
managers, and Overwing's reported installed-binary source lag. Git has no local
identity or core.worktree override. Worker scratch is removed after checks join.
The shared build cache is retained.

STATUS: CONTINUE
