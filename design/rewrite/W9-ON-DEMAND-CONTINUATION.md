# W9 on-demand controller continuation

The local run checks controller readiness after source preparation and directly
before CreateRun. Constructing its transport no longer starts an idle controller
before compilation. Foreground and detached runs share this ordering. Failure
stops intake and preserves the startup error.

The CLI's single EnsureStarted call is in ensureLocalController. General local
API connections, run submission, doctor, schedules, hidden timer ticks, queue
priority and daemon restart use it. The operation checks the captured home,
cancellation, running status, PID and version. Errors begin with
`cannot start local controller` and retain their underlying cause. Dashboard
start and service installation keep their lifecycle transitions. The controller's
own private transport is constructed while binding its server and does not
recursively start itself.

Deleted the separate schedule startup helper and duplicate doctor/client startup
checks. No whole production file was deleted. Production Go this continuation:
**+68/-47, net +21**. W9-owned production versus main: **+16,916/-23,812,
net -6,896**, within the -2,900 budget. Counts exclude tests, testdata and contract
declarations.

The local-execution guide and its generated mirror describe the submission
ordering and startup errors. No foreign contract or lead-owned handoff changed.
Shared files touched: docs/local-execution.md and its generated pkg/docs mirror.
Other changes are in cmd/sparkwing.

## Evidence

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9/go and TMPDIR=/tmp/sw-astra-W9:

- `go build -p 4 ./...` and `go vet -p 4 ./...` in both the root and .sparkwing modules.
- Focused CLI controller readiness, local run, doctor, schedules, daemon and
  queue tests, including under race.
- Full short suites for internal/doccheck, internal/clisurface,
  pkg/controller/client and internal/controllerwire.
- `go run ./internal/commentcheck -base main .`, clean across 2,534 files.
- `bin/sync-docs.sh --check` and `git diff --check`.

TestLocalControllerOptionsNamesStartupFailure ran red on the unlabelled startup
error, then passed. The shared-operation tests cover resolution failure, wrong
home, missing lifecycle, failed startup, stopped status, missing PID/version and
cancellation. TestLocalRunEnsuresControllerAfterPreparationBeforeIntake verifies
ordering and that startup failure prevents submission.

TestRunStartsLocalControllerBeforeSubmitting builds the CLI, invokes the real
`run example --sw-detached -o=json` path with isolated HOME/config/state, checks
that no controller was present, requires a run receipt and a ready controller,
then reads the submitted run through the CLI. Cleanup stops its controller.
It skips only when a socket probe returns EPERM or EACCES. This sandbox returned
`setsockopt: operation not permitted`, so native execution remains with the lead:

```sh
GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go test -p 4 -tags rewrite_integration ./cmd/sparkwing \
  -run '^TestRunStartsLocalControllerBeforeSubmitting$' -v -count=1
```

No acceptance cell is newly claimed and no archived test was newly ported.
The reported native failure could not be reproduced here because sockets are
refused. This checkout already had startup in controllerOptions before the
change, so the lead should rebuild the candidate before its native rerun.

## Scope and friction

Mechanism added: a native local-run readiness callback at the submission boundary,
using the existing lifecycle. Starting only during runtime resolution leaves a
gap across source preparation. No flag, environment variable, retry loop, timeout,
service or compatibility path was added. Removing that callback reverses it.

Contract changes needed: none. Open question: native test outcome. Independent
landing review remains with the lead. No Git write, candidate installation,
operational Sparkwing CLI, pipeline, release or deployment ran. Git has no local
user identity or core.worktree override. Scratch is removed after checks join.
The shared /tmp/sw-gocache is retained. Friction: sandbox socket refusal and the
installed Overwing binary's source lag.

STATUS: COMPLETE
