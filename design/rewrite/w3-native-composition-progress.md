# W3 native store composition

<!-- design:begin -->

W3 remains incomplete because production placement composition and native
acceptance are open. The legacy Session, Server, scheduler, layers, trigger
consumer and pod host remain deleted.

The shared native host now has one composition test for owner and claimed work.
It uses real SQLite intake, award issuance, plan acceptance, scheduling,
reconciliation, execution starts, fenced step writes and run completion. Source
acquisition reads an actual local Git checkout and the captured environment.
The real Go SDK runs through ProtocolSession. Process delegation uses the real
store authentication snapshot, binding checker and capability validators.

OutputRecords publishes through a scripted byte transport into real upload
reservations, receipts, records and terminal result attachment. Engine
resolution/install and local admission remain contract fakes. The test makes no
HTTP, filesystem-byte-backend or deployment claim. Both roles end with one
successful attempt, the sealed engine, closed SDK compute and no live process
delegation. The claimed case resumes its owner to finish the accepted run.

The old fake-owner composition test and its hand-written state machine are
deleted, removing 175 test lines. Existing store fixtures share intake setup
instead of copying it. No second SDK, store or byte fake is introduced.

The queue renderer now calls nodehost's scope-label function beside the tuple
codec. The orchestrator label wrapper and Paths aliases are deleted. Their
tests move to nodehost and paths. The CLI scope fixture uses current encoded
keys instead of the retired g:/b: encoding. No compatibility decoder is added.

| Production comparison | Added | Deleted | Net |
| --- | ---: | ---: | ---: |
| This session against HEAD 154ad2a95 | 19 | 32 | -13 |
| W3 against main | 10,961 | 25,924 | -14,963 |

W3 is 12,663 lines below its -2,300 budget. The remaining orchestrator tree has
673 production Go lines, including its metrics and receipt subpackages. The
remaining root helpers cover source revision lookup, profile refusal and run
handles. None implements run hosting.

Unit evidence: the full nodehost, lease, source, orchestrator and paths suites
pass. The native composition and relocated scope/property tests pass under the
race detector. Focused CLI queue, local owner and host-state tests pass.
Existing lifecycle and recovery acceptance selections pass. No acceptance
registry or cell is added or activated. Existing 09:40 items 1-3 and 07:40 item 9
remain complete.

| W3 amendment assignments | Disposition |
| --- | --- |
| A1-01/02/05/21-25 | Existing service registration, masking and authority suites pass. The new composition also uses real parent authentication and delegation validators. Native HTTP and placement services remain open. |
| A1-03/04/06/07 | Existing error, record-limit, cancellation and optional-concurrency consumers pass. |
| A1-10/11/12/19 | Actual acquired source and captured process environment reach the native host. Engine resolution/install are fakes in this proof. Matching-engine host execution remains open. |
| A1-13-18 | Reconciliation, failure, expansion replay and attempt-fence suites pass. The private remote snapshot and run-start transport remain missing. |
| A1-36 | Actual output reservations, receipts and record attachment compose with the host. The byte transfer is scripted. W1b's materialization path is untouched. |
| A1-38/39/50/51 | Owner and claimed execution use one native executor with real store/SDK evidence. Production placement factories remain open. Legacy hosting remains deleted. |
| A1-44-48 | Existing sealed options, argument classifications, environment and guard consumers pass. The new composition loads the captured environment through source and SDK startup. |
| A1-53/54 | Full placement cells remain blocked. Approval SkipIf still needs the missing C1 owner-success edge. |

Manual checks use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR/TMPDIR under /tmp/sw-astra-W3, GOMAXPROCS=4
and package parallelism at most four. Reproduce from this worktree:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator ./internal/paths ./internal/web ./cmd/sparkwing
go test -p 4 ./internal/nodehost ./internal/lease ./internal/source ./internal/orchestrator ./internal/paths -count=1 -timeout 3m
go test -race -p 4 ./internal/nodehost -run '^(TestNativeHostFromIntakeThroughSDKAndPersistedCompletion|TestScopeKey|FuzzConcurrencyKey)' -count=1 -timeout 2m
go test -p 4 ./cmd/sparkwing -run '^(TestQueue|Test.*Queue|TestLocal|TestOwner|TestCLIHost)' -count=1 -timeout 2m
go test -p 4 ./tests/acceptance -run '^(TestLifecycle|TestRecovery)$' -count=1 -timeout 1m
go test -p 4 ./internal/web -run '^$'
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

Whole-repository build, scoped vet, commentcheck, sleepcheck, formatting and
diff whitespace checks pass. Web tests are compile-checked because only their
Paths import changes. Their socket/schema runtime is outside this change.

Contract changes needed in other areas remain:

- W9/W2 must expose the private execution snapshot and run-start operation.
  Controller.ReadSnapshot returns only ScheduleSnapshot. cliHostState still
  needs its privateHostState adapter for complete run/job state and transitions.
- W1 must declare owner policy-failure and approval SkipIf success edges.
  NodeTransitionEdges still grants approval resolution only to the operator.
- W8/W9 must declare and compose selected-engine host execution. The installer
  contract exists. Resolving an engine identity alone does not launch that
  engine as the host. Separate pipeline/execution-source acquisition also needs
  its sealed contract before W3 can implement it.

Production local run and local-controller factories still return unavailable.
Runner, launcher and local composition must bind source authority, subscriptions,
storage, captured environments and delegated services. HTTP/api.sock,
PostgreSQL, real byte backends, Windows/Darwin and deployment/repository QA
remain untested. W1b's exclusive materialization path is untouched.

Shared files changed are cmd/sparkwing/queue.go, queue_contract_test.go,
internal/web/server_test.go, internal/paths/state_test.go, C3 prose and the port
inventory. The first three are minimal caller/fixture migrations. No foreign
contract, public command, setting, environment variable, service or customer
limit changes. No mechanism is added. Lead-owned handoffs/amendments are
unchanged. No Git write or Sparkwing runtime ran. Scratch is removed after
checks join. The shared build cache remains.

Friction: the stale queue-key fixture failed its first focused run. Updating
the fixture to the shared constructor resolves it. Native composition required
the declared output publisher even for explicit null, and result keys must come
from blob.ResultRecordKey. The real store proof now exercises those requirements.

<!-- design:end -->

STATUS: CONTINUE
