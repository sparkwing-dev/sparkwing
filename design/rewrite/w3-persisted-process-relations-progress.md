# W3 persisted process relations

<!-- design:begin -->

The native process binding adapter now uses its fresh persisted controller
parent for store relation checks. Its memory registry still checks the exact
process principal, run, job, team and attempt before consulting persistence.
This removes the 401 from live mask registration because the store cannot
resolve an in-memory node-host credential as a persisted authority.

The invariant is that a process reaches only its exact fence and resources
allowed by its live persisted parent. A real SQLite and PolicyRouter regression
reproduced the failure before the one-line fix. Its 128 generated cases vary
run, job, team and attempt selectors. It also verifies process revocation before
terminal reporting and refusal of later mask registration. Process liveness
ends through revocation, which the native executor performs before its terminal
report. No new credential lifetime rule is introduced.

The existing owner/claimed composition now registers masks through the actual
policy router, shared authenticator, process registry and SQLite relation
checker. Source acquisition, SDK pipes, result bytes, receipts and terminal
attachment retain their prior real implementations. Engine resolution/install,
admission, source metadata adapters and signed-byte transport remain scripted
boundaries. HTTP handlers run in-process because this worker cannot open sockets.

Two duplicate authority-test fakes are deleted. The existing scripted parent
and binding callback cover their callers. The change removes 21 test lines and
adds seven in host_process_authority_test.go. No legacy production code is
deleted in this session because Session, Server, scheduler, layers and their
hosting callers were already removed. Lead 09:40 items 1-3 and 07:40 item 9
remain complete. No host_* harness file remains in internal/acceptance.

Production delta against HEAD a7c3fc7c4 is **0**: two lines replaced, including
the adapter's API comment. W3 against main is **-14,963**: 10,961 added and
25,924 deleted. This remains 12,663 lines below the -2,300 budget. No production
mechanism, interface, setting or environment variable is added.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W3/gotmp and
TMPDIR=/tmp/sw-astra-W3/tmp. Reproduce with go -C and this worktree's absolute
path:

```text
go build -p 4 ./...
go vet -p 4 ./internal/nodehost
go test -p 4 ./internal/nodehost -count=1 -timeout 3m
go test -race -p 4 ./internal/nodehost -run '^(TestProcessAuthority|TestNativeHostFromIntakeThroughSDKAndPersistedCompletion)' -count=1 -timeout 2m
go test -p 4 ./internal/runners ./internal/runners/launcher ./tests/acceptance -run '^(TestHosting|TestNodePreparation|TestManagedRunner|TestBootstrap|TestLifecycle|TestRecovery)' -count=1 -timeout 2m
go run -p 4 ./internal/commentcheck -base main .
go run -p 4 ./internal/sleepcheck -base HEAD .
```

The full nodehost suite, repository build, scoped vet, targeted race checks,
runner/launcher selections, lifecycle/recovery acceptance, commentcheck and
sleepcheck pass. Formatting and diff whitespace checks pass.
No acceptance registry changes. No native placement cell is activated and no
additional archived test port is claimed.

W3 remains incomplete. The outstanding amendment audit is unchanged except for
the real router/store composition of A1-01/02/21-23 above. Other area's contract
changes remain required:

- W9/W2 must expose complete private execution snapshots and run-start transport.
- W1 must supply the owner policy-failure and approval SkipIf success edges.
- W8/W9 must declare selected-engine host invocation and separate sealed pipeline
  and execution-source acquisition.
- W1/W2 must preserve EndLine and Column in annotation persistence. The node-host
  request and W9's AnnotationWrite carry both fields. C1 AnnotationWrite and C2's
  annotationRequest/handleAnnotation carry only Line, so a final service writer
  cannot preserve the complete location through those contracts.
- W1/W2/W9 must supply an authority-bearing usage writer and transport before
  native usage/profile recording can be connected. The retained AddNodeUsage
  takes run/job IDs and NodeUsage with a legacy context fence. The final
  NodeTransition/NodeResult and controller wire expose no typed usage report.

Production local/runner/launcher factories still need source authority, state
subscriptions, captured environments, byte bindings and delegated service
writers. C2's production DataAuthority resolver, native HTTP/api.sock, platform,
Postgres, S3 and repository/deployment acceptance remain open. W1b's exclusive
materialization path is untouched. The prior report lists the remaining
assignments in [its amendment audit](w3-native-composition-progress.md).

Shared files touched are c3-protocol-sdk.md and t1-port-inventory.md. No foreign
production caller or contract is changed. Lead-owned handoffs and amendments
are unchanged. No Git write or Sparkwing runtime ran. Checks joined before
worker scratch was removed. The shared build cache remains.

Friction: earlier component fixtures supplied an always-successful relation
checker, which hid the real store's refusal of memory-only credential prefixes.
The complete placement factories and the named foreign contracts remain the
next integration work. No new open design question is raised.

<!-- design:end -->

STATUS: CONTINUE
