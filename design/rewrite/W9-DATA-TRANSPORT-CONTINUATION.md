# W9 data transport continuation

Base HEAD: `a8635b293`. The lead's 01:40 native local-controller, local-schedule
and public-doctor passes remain the starting evidence. The SDK replacement pin
policy is unchanged. No merge conflicts remain at this base.

## Implemented and deleted

The shared record client now accepts the server-selected verified default ref
when `RecordLookup.VerifiedDefaultOnly` is true. Previously it rejected every
successful default-ref substitution as a changed key. The invariant is that
only the ref may change during this selection: repository, kind, name, pipeline,
job and key must still match. The returned ref must be nonempty and verified.
Ordinary lookups retain exact key matching. The regression failed before the fix
and passes through the actual HTTP router and shared client with fake storage.

Malformed percent escaping in record selectors now returns canonical
`bad_request` before storage access. The existing acceptance test reproduced a
500 for `%xx`. Its query subtests and the new generated-input test cover that
path, incomplete escapes and arbitrary selector values.

Four duplicate server record DTOs and two anonymous client declarations are
removed: 56 declaration lines. Their consumers use the existing shared
DataRecordWriteRequest, DataRecordLookupResult, DataRecord and DataMember types.
The record JSON shape is unchanged. No aliases or replacement compatibility
types were added, and no whole production file was deleted.

The API reference and OpenAPI are regenerated. They now include the eleven
merged `owner_process` authorization alternatives that the generated files had
omitted. This updates documentation to the existing policy without changing
grants. The data guide, C3 contract clarification, changelog and embedded mirrors
also match the implementation.

## Evidence

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go, TMPDIR=/tmp/sw-astra-W9 and at most four package
workers per command:

- Whole-repository build before and after production changes.
- Focused vet for controller/client, controller and acceptance packages.
- Full short controller/client suite and the data/surface acceptance selection.
- Native-data, private-host, numeric-selector and debug controller selections.
- Existing `TestStorage/storage-local-remote-cache-opt-in` component cells.
- New selector and malformed-query properties, HTTP client regression and private
  cache response property under race.
- Selector fuzzing: 30,434 executions. Malformed-query fuzzing: 4,225 executions.
- API policy inventory/generated-spec tests, API reference tests, full doccheck,
  embedded docs/privacy/symbol checks, changelog check and whitespace check.
- Commentcheck: clean across 2,485 files.

The full pkg/docs suite was attempted and failed when its WebClient test opened
a TCP listener: `socket: operation not permitted`. The suite excluding those
socket-based WebClient tests passes. No native placement cell was newly enabled
or claimed, and no archived test was newly ported. The new HTTP test is component
evidence with real router/client and fake storage, not a completed local run.

Independent read-only review found no blocking correctness or security issue and
no mechanism to remove. Its seed-coverage suggestion is applied to every selector
mutation and an ordinary exact-ref success. Landing and native review remain
with the lead.

## Remaining integration and contract requests

The foreign boundaries recorded in W9-MERGE-RECEIPT-CONTINUATION.md still apply:

- W8 must supply production CloudBoundary network evidence and reservation-scoped
  teardown before the shipped launch factory can be composed. ProductionRuntime
  still has no Launcher binding. The known-red shipped-runner launch test was
  not rerun against an unchanged runtime. Start, warm, fetch-source and run-node
  retain the prior production-HTTP evidence.
- W2/W3 must finish persisted owner/eval source-reference authorization.
  `bindingPipelineReferenceTx` still requires UnitNode and its current attempt.
  UnitEval's frozen grant table also omits NodeReport, which its mask and cleanup
  service routes require. No work attempt or grant was invented.
- W1/W2 must expose an atomic deletion result and absent-run replay selectors
  for RunDeletion. The store writer remains error-only while HTTP returns 204.
- Retry-history reads still use the legacy run reader, and receipts still need
  settled billing and complete seal-identity projections.
- The public remote-cache transport still lacks the canonical read-set/source
  contract. C3 and the privacy acceptance property keep uploader, read-set, App
  delivery and SHA facts private. W7's RemoteCache requires full verified source
  evidence. The ref-selection fix does not manufacture that evidence or complete
  --sw-cache-from. The lead must reconcile those contracts before that adapter
  can be completed. No private field was exposed.
- Here eligibility, publication, Cloud plan/eval/bootstrap source consumption,
  retained scheduled intake and native platform evidence remain as listed in the
  prior report. Korey's SDK pin decision remains reserved.

W8 findings 6-9 and local peer intake retain their merged implementations. This
continuation changes their record DTO consumers and regenerates owner-process
documentation. Lead-owned handoffs and amendments remain unchanged.

## Scope and size

Production Go delta this session: **+32/-74, net -42**. W9-owned production
versus main: **+16,904/-23,797, net -6,893**, within the -2,900 budget.

Shared files touched: pkg/controller/data_record_routes.go,
pkg/controller/data_cache_routes.go, pkg/controller/data_request_binding.go,
tests/acceptance/data_cache_http_test.go, tests/acceptance/data_writes_http_test.go,
api/openapi.yaml and the generated pkg/docs copies. No caller outside those
files needed migration. C3 changed only its own prose clarification, not a foreign
declaration or wire schema.

Mechanisms added: none. Friction: sandbox sockets and the foreign contracts above.
Overwing reports its installed binary is ten commits behind source. No Git write,
pipeline, candidate install, operational Sparkwing run or release ran. Git has no
local identity or core.worktree override. Worker scratch is removed after checks
join, and /tmp/sw-gocache is retained.

STATUS: CONTINUE
