# W3 native store and byte composition

<!-- design:begin -->

W3 remains incomplete because production placement composition and native
acceptance are open. Session, Server, the legacy scheduler and layers, trigger
consumer and pod host remain deleted. Lead decisions 09:40 items 1-3 and 07:40
item 9 remain complete.

The existing owner/claimed composition now publishes a typed SDK result through
NewSignedStore, TransferClient and the real filesystem HTTP handler. Signed PUT
checks size and digest before immutable CAS publication. Real tenant operations
reserve the upload, settle its receipt, commit the output record and attach the
terminal result. The test checks the exact stored JSON and empty staging
directory. It retains real SQLite intake, claims, acceptance, step writes,
completion, local Git acquisition, captured environments and process delegation.

The former scripted Store.Put callback is replaced by one scripted DataSigner
that calls the real tenant and filesystem operations. The shared signed client
owns byte transfer and client verification. The in-process HTTP transport,
engine resolution, SDK installation, local admission and upload-binding assembly
remain test adapters. No native HTTP listener, production /data binding or
placement cell is claimed. The fixture reuses its existing typed-output job.

| Production comparison | Added | Deleted | Net |
| --- | ---: | ---: | ---: |
| This session against HEAD 5b8d495cc | 0 | 0 | 0 |
| W3 against main | 10,961 | 25,924 | -14,963 |

W3 is 12,663 production lines below its -2,300 budget. This session removes
22 Go test lines from the prior composition while replacing the fake byte path.
The legacy hosting deletion was already committed. The remaining orchestrator
helpers cover source revision lookup, profile refusal, run handles, metrics and
receipts. None implements run hosting. No additional archived test port is
claimed.

| W3 amendment assignments | Disposition |
| --- | --- |
| A1-01/02/05/21-25 | Service, masking, authority and real store delegation suites remain covered. Production HTTP/service bindings remain open. |
| A1-03/04/06/07 | Existing error, record-limit, cancellation and optional-concurrency consumers remain implemented. |
| A1-10/11/12/19 | Acquired source, captured environments and granted identity reach the native host. Matching-engine host invocation needs its foreign contract. |
| A1-13-18 | Reconciliation, persisted failures, expansion replay and attempt fences remain implemented. Full remote snapshots and run-start transport remain open. |
| A1-36 | Typed SDK output now reaches real filesystem bytes, receipts, records and terminal attachment through the shared signed client. Production data binding remains open. W1b's materialization path is untouched. |
| A1-38/39/50/51 | Owner and claimed work share one native executor. Production placement factories remain open. Legacy hosting remains deleted. |
| A1-44-48 | Existing sealed options, argument classifications, captured environment and guard consumers remain implemented. |
| A1-53/54 | Native placement acceptance remains blocked. Approval SkipIf needs the C1 owner-success edge. |

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3/gotmp,
TMPDIR=/tmp/sw-astra-W3/tmp, GOMAXPROCS=4 and package parallelism at most four.
Run these commands from the worktree, or use go -C with its absolute path:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/nodehost/testdata/stepsfixture
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator/... -count=1 -timeout 3m
go test -p 4 ./internal/orchestrator/nodemetrics -count=3 -timeout 30s
go test -race -p 4 ./internal/nodehost -run '^TestNativeHostFromIntakeThroughSDKAndPersistedCompletion$' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^(TestLifecycle|TestRecovery)$' -count=1 -timeout 1m
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

Whole-repository build, scoped vet, nodehost, lease, source, orchestrator,
receipt, native composition race test, existing lifecycle/recovery acceptance,
commentcheck, sleepcheck, formatting and diff whitespace checks pass. The broad
W3 run failed two unchanged native metrics tests with "session snapshot
unavailable". Both pass five isolated repetitions. The complete metrics suite
also passes three isolated repetitions. No acceptance registry or cell is added
or activated. The lead retains full native acceptance and pipeline gates.

Contract changes needed in other areas remain:

- W9/W2 must expose the private execution snapshot and run-start operation.
  Controller.ReadSnapshot returns only ScheduleSnapshot. cliHostState still
  needs privateHostState for complete run/job state and transitions.
- W1 must declare owner policy-failure and approval SkipIf success edges.
  Approval resolution in NodeTransitionEdges still requires the operator.
- W8/W9 must declare selected-engine host invocation and the separate sealed
  pipeline/execution-source acquisition contract. SDK installation is declared,
  but selecting an engine identity does not execute that engine as the host.

Production local run and local-controller factories still return unavailable.
Runner, launcher and local composition must supply source authority, subscriptions,
storage, captured environments and delegated services. The scripted signer does
not supply C2's production DataAuthority resolver or authenticated /data routes.
HTTP/api.sock listeners, PostgreSQL, S3, Windows/Darwin and deployment/repository
QA remain untested. W1b retains exclusive materialization ownership.

Shared files touched are C3 prose and the test port inventory. No production
caller, foreign contract, public command, setting, environment variable or
customer limit changes. No production mechanism is added. Lead-owned handoffs
and amendments are unchanged. No Git writes or Sparkwing runtime ran. Scratch
is removed after checks join. The shared build cache remains.

Friction: native metrics snapshots failed during one broad run and did not
reproduce in the isolated repeats. Their cause remains unproven. No metrics
code is changed. The production data-authority resolver remains absent. The component
scripts that contract boundary while exercising the real signed client, store
transactions and filesystem bytes. It does not substitute a claimed production
binding or reopen W1b's materialization work.

<!-- design:end -->

STATUS: CONTINUE
