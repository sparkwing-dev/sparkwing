# W9 readiness continuation

Base: `f7966c08c`. Priorities (1)-(3) are implemented. This continuation closes
the remaining consumer-start ordering gap and makes runtime-fixture cleanup
unconditional. Native command reruns remain with the lead. The full W9 area
remains open at the foreign contract boundaries below.

## Implemented

The elected runtime now enters its private API accept loop and publishes
readiness before starting controller consumers. Previously consumers started
first, so queued work could call private state and storage services before their
API was serving. Pipeline compilation remains independent of readiness. The
change moves three existing lines and adds no production mechanism or timeout.

`newRuntimeFixture` registers drain and join in `t.Cleanup` for every active
runtime it constructs. Cleanup runs before the fixture's home is removed.
Consumers and HTTP workers join before the controller closes its store.

The local-execution guide describes startup ordering, doctor's unrecorded-outcome
warning and managed-service handover. It no longer claims every native factory
is unbound. The embedded mirror was regenerated with `bin/sync-docs.sh`.

## Evidence

Both new regressions were run red before their fixes:

- `TestLocalRuntimeDefersConsumersUntilPrivateAPIReady` failed with
  `consumer started before the private API was serving`.
- `TestLocalRuntimeFixtureCleanupJoinsConsumers` failed because its consumer
  remained running and its store remained open after subtest cleanup.

Both now pass, including under the race detector. The existing readiness test
also passes with consumer preparation blocked. These tests use the real runtime
and in-memory listener boundaries. They open no sockets and use no sleeps.

All Go commands use `GOWORK=off GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOTMPDIR=/tmp/sw-astra-W9/go` and at most `-p 4`.
From this repository, the following passed:

- `go build ./...`.
- `go vet -p 4 ./internal/wingd ./internal/wingd/localcontroller ./internal/wingd/client ./cmd/sparkwing`.
- `go test -p 4 -race ./internal/wingd -run '^TestLocalRuntime' -count=1`.
- Full short suites for localcontroller, cluster, controller/client and runners.
- Focused CLI doctor, detached entrypoint, local queue and production-controller
  construction tests.
- Controller doctor, peer-intake commit property, private host, persisted
  storage/log reader and service-installation handover regressions.
- `TestEmbeddedOwnerSealsLogsDuringCancelledHostCleanup` and all modes of
  `TestNativeStoreOwnerW8` in `tests/acceptance`.
- The complete `internal/doccheck` suite, embedded-doc symbol/privacy/section
  tests and `bin/sync-docs.sh --check`.
- Tagged CLI and cluster compilation with `-tags rewrite_integration -run '^$'`.
- `go run ./internal/commentcheck -base main .`: clean across 2,337 files.
- `git diff --check`.

The full `pkg/docs` suite could not run: its web-client fixture calls
`httptest.NewServer`, which is refused with `socket: operation not permitted`.
Its relevant socket-free tests passed. No native acceptance cell is newly
claimed. No archived test was newly ported.

## Contract changes needed and real integration left open

No foreign contract was changed. The requests in
[W9-HANDOFF.md](W9-HANDOFF.md) and
[W9-QUEUE-CONTINUATION.md](W9-QUEUE-CONTINUATION.md) remain open.

1. **C2/W3 owner/eval process authority.** The invariant is that an owner closure
   retains its owner or eval-unit lease and source-job relation, independently of
   any node-attempt award. `CredentialNodeProcess` accepts only a positive node
   attempt. `ProcessAuthorities.Delegate`, `NativeProcesses.Delegate` and
   `processServices` couple the SDK job to that node binding. C2/W3 must declare
   the process credential/binding and source relation before a production
   `NativeConfig.Evaluate` adapter can implement it. The existing separate-binder
   test uses the declared interface with a fake. No attempt is fabricated.
2. **W8 Cloud deployment boundary.** `KubernetesBoundary.Probe` and `Clear` remain
   required callbacks without production implementations. They must prove
   network enforcement and reservation-scoped teardown. The shipped runtime's
   launcher factory cannot compose them from manifests or a no-op. The installed
   runner's tagged launch regression remains open. Cloud plan/eval dispatch also
   needs the shared host entrypoints for those unit kinds.
3. **W9 log wire with W2's reader.** `client.NewLogReader` expects tagged
   `LogPage` messages, while the public persisted-log handler serves bytes.
   Canonical subjects, cursor state and finalization must come from the server.
   Parsing rendered text would invent those facts. The shared writer and
   record/object path remain implemented and tested separately.

Canonical remote placement eligibility, cross-run record/read-set proofs,
bootstrap checkout consumption, sealed scheduled intake and the earlier
here/cache/publication integrations remain as recorded in those reports.

The lead's native rerun should select `TestReviewProductionExecutionBindings`,
`TestReviewPublicDoctorSweepsAndWarnsForPrunedRun` and
`TestLocalClaimantW8UsesPersistedPeerAuthority`, with an isolated HOME and state
and a release-version stamp. `TestInstalledRunnerRuntimeComposition/launch`
still requires the Cloud deployment boundary.

## Scope, size and friction

Shared files touched: `internal/wingd/local_runtime.go` and
`internal/wingd/local_runtime_test.go`. Documentation changes are
`docs/local-execution.md` and its generated mirror. Lead-owned handoffs and
amendments are unchanged.

Production delta this continuation is +3/-3, net zero. No whole legacy file was
deleted. W9-owned production versus main remains +16,876/-23,797, net **-6,921**,
within its -2,900 budget. Tests, acceptance and contract declarations are excluded.

No new option, environment variable, interface, retry, compatibility path or
production mechanism was added. No Git write, operational Sparkwing command,
candidate install, pipeline, native service operation or release ran. Independent
landing review remains with the lead. Worker scratch is removed after checks
join. `/tmp/sw-gocache` is retained. The process sweep found no worker-owned
daemon or child left running.

STATUS: CONTINUE
