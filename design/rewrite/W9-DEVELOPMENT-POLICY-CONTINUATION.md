# W9 development runner policy

The local CLI supplies its own embedded build version to
`SignedRunnerAdmission`. At the single runner-policy fetch boundary,
`buildinfo.IsReleaseVersion` classifies that build. A development build skips
the fetch and logs once that version policy checks are off. An unset build
identity retains enforcement. Released builds keep the existing resolver,
signature verification, fallback and runner evaluation path.

Invariant: runner-supplied version strings cannot disable policy enforcement in
a released CLI. A generated-input property test covers that invariant. The
build-kind test proves zero HTTP requests and one log line for development
builds, and the original HTTP 403 failure for released or unconfigured builds.
The local composition test checks that the policy receives the CLI identity.

The production in-process run lifecycle passes admission, owner and node claims,
plan acceptance, transitions, output storage, log publication and completion.
No acceptance cells were newly unskipped and no archived tests were ported.

A disposable queue diagnostic used the real controller, claimant, store runner
and native service binder, with the controller HTTP transport over `net.Pipe`.
It supplied a sealed environment, this checkout and its full commit ID, and
stopped after one claim. No signed-policy fixture was installed. The queue
passed policy checking, owner authentication and data authority, then failed at
`internal/wingd/localcontroller/native.go` when opening its process API listener:
`wingd: local queue: listen tcp 127.0.0.1:0: socket: operation not permitted`.
The temporary diagnostic was removed. Further native execution needs the lead's
socket-capable shell. No later product failure is established by this probe.

Both modules pass `go build -p 4 ./...` and `go vet -p 4 ./...`. Focused controller
and CLI tests, the runners and localcontroller short suites, commentcheck against
main, docs mirror drift and diff whitespace checks pass. Go commands used
`GOWORK=off`, `GOFLAGS=-buildvcs=false`, `GOCACHE=/tmp/sw-gocache`, and scratch
under `/tmp/sw-astra-W9`. Scratch was deleted after checks joined.

Production changes this session are +15/-4 lines, net +11. W9-owned production
Go versus main is +16,916/-23,812, net -6,896, within the -2,900 budget. No legacy
subsystem was replaced. Shared files touched are `pkg/controller/engine_claim.go`,
its new `runner_admission_test.go`, and the generated docs mirror. The exported
policy type gains `BuildVersion` and uses a pointer receiver because its one-time
notice is shared by queue and HTTP checks. Wire and store contracts are unchanged.
No foreign contract changes are needed. No repository Git writes ran.

The only added state is the one-time log guard required by the development-build
decision. Open questions: none for this task. Friction: the sandbox blocks the
native process API listener. Released-build policy decisions remain with Korey.

STATUS: COMPLETE
