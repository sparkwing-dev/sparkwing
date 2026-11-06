# Local runner migration progress

Uncommitted continuation note. Step 3 remains incomplete. Do not land or push.

Commits: `3252fbd5e` retries fresh claim polls across five seconds of consecutive
controller transport failures (missing sockets/pipes included) and exposes
`Loop.Drain`. Successful responses reset recovery. Claims are never replayed.
Held hosts survive outages and drain joins them without cancelling their contexts.

`652aedc23` captures observed job process exit before final log acknowledgement,
cleanup and artifact/cache publication. Optional `X-Sparkwing-Exited-At` carries
it to the controller. Older controllers ignore the header. Command JSON and
persisted result replay identity stay unchanged. First committed observations
survive replay. `ce2f36095` updates source docs, owners/guards and changelog.
No SDK or store-schema changes. No detached runner is launched yet.

Final passing checks: full runners/client/controllerwire/runstate suites;
focused store/controller/nodehost reporting regressions; golangci fmt; lint of
all seven changed packages (0 issues, depguard included); commentcheck;
sleepcheck; doccheck package tests and actual source-doc drift check; docs sync;
changelog check; full .sparkwing/jobs (128.199s). One test binary at a time, -p 4,
GOMAXPROCS=4. Broad store suite timed out at 300s in unrelated membership
property coverage. Full gate remains the lead's work. Jobs initially failed on
this note's Markdown list spacing, then passed after correction.

Independent reviews resolved legacy replay hash compatibility, retry-backoff
cancellation reusing the prior timestamp, and capture delayed by output drain.
Final frozen code commits have no remaining review findings. The HTTP reporting
test rebuilds a handler over the same store, not a real controller process.
The protocol observation test uses a real child with blocked output acknowledgement.

Remaining: durable runner PID/start/version records and atomic roll; supervisor
and OS launcher survival (systemd user service, confirmed-no-systemd session,
launchd, Windows SCM); HTTP execution-host migration; orphan process-group cleanup;
measured cancel-to-process-exit; local feature tests before embedded-path deletion.

Lead decision: remote here still loads one-time authority in CLI, claims a named
trigger on the selected remote controller, then hosts it. Local peer queue cannot
discover this work. Define a verified-peer local handoff retaining that remote
controller and narrow authority, or explicitly defer remote here. The proposal
requires porting here but leaves that transfer unspecified. No broker was added.

Migration facts: preserve source Base.Local, LocalOnly engine resolver, captured
home/environments and socket BundleTransport. Generation-zero local_foreground
triggers reserve initial claim for the named CLI claimant (store/claim_query.go).
Foreground output already follows committed segmented logs. Cache/publication
helpers remain in cmd/sparkwing/local_run.go and local_publish.go. Extract machine
labels from claimant without widening award authority.

All product execution used isolated HOME/SPARKWING_HOME. Private HOME needed the
real Go installation on PATH. Go 1.26.10 auto-selected from the 1.26.9 installation.
Task scratch was removed at session end. No landing, push or service-manager tests.
