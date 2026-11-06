# Local runner migration progress

Step 3 remains incomplete. Do not land or push. Keep this note uncommitted.

Prior session: `3252fbd5e` adds five-second claim transport recovery and graceful
`Loop.Drain`. `652aedc23` reports runner-observed exits using optional
`X-Sparkwing-Exited-At`, preserving existing command JSON and replay identity.
`ce2f36095` documents those contracts. The lead gate passed that session.

This session: `558d50b1f`, `2cf25dd97`, `5fa90b2d9` add orphan session cleanup
using the existing durable admission holder and measurement PID/start identities.
The existing liveness worker inspects and kills sessions outside the admission
mutex. Dead holders retain capacity until cleanup acknowledges emptiness.
Receipts with roots survive pruning. Cancelled measured leases retain capacity
until host release after joining the job. Cleanup observes controller cancellation
between roots. Existing procgroup session kill has its own bounded wait and no
context variant; the call has a safety nolint matching existing package precedent.
No SDK, schema or wire-field change. Detached runner startup is still absent.

Linux tests use a real holder spawning a shell and child in a separate session.
They cover holder death, SQLite close/reopen, failed cleanup acknowledgement,
receipt retention, live holders, PID/start mismatch, capacity during cancellation
and a cancelled cleanup scan. Nodehost measures cancel-to-exit through the real
verified API socket and an executing protocol child, without award renewal.
Latest raw measurement: 1.237601ms. Speed is logged, not asserted, because sleepcheck
rejects wall-clock gates. Actual process exit and lease.CancelError are required.

Checks passed: full wingd (31.781s), full nodehost (55.935s), final focused
nodehost cancellation rerun, full .sparkwing/jobs (89.371s), doccheck package,
commentcheck and sleepcheck. Final golangci fmt/lint (depguard included, zero
issues), actual source-doc drift, docs sync and changelog checks also passed.
The wingd test package cross-compiled for windows/amd64 and darwin/arm64.
Independent static review passed all three frozen commits with no findings.
Native macOS/Windows behavior and service survival remain unverified. Windows
cleanup waits for the runner-owned kill-on-close Job Object to close; it does
not introduce a controller-owned job handle. No full gate run in this lane.

Remaining: durable runner instance PID/start/version and accepting/draining state;
atomic activation plus claim gating; process supervision and OS launchers;
HTTP execution-host migration; feature parity tests; removal of embedded paths;
real controller restart/roll and native service acceptance.

Recommended next slice: extract the award-bound host composition in
internal/cluster/production_runtime.go for reuse by embedded and detached local
hosts. Preserve local source Base.Local/Captured, LocalOnly engine resolver,
workspace/source and sealed environment snapshots, cache/publication, BundleTransport
and engine pins. Delete direct host store bindings after feature parity tests.
Keep cluster's Cloud Docker and Actions behavior in its existing owner.

Foreground contract: generation-zero local_foreground triggers require named
claims by their creating principal (store/claim_query.go). An ordinary unnamed
loop cannot claim these. A verified-peer runner handoff must retain this named
claim and the shared loop's host accounting. Remote here separately retains a
run-bound bearer, selects a remote controller and makes a named trigger claim.
Transfer that narrow authority and selected transport to the runner; local peer
authority cannot replace it. Burst local secrets must come from submitted sealed
snapshots/transfer, not the detached runner's ambient environment.

Friction resolved: protocol fixture needed complete hello and settled-step fields;
its raw cat predecessor exited before registration. Lint required controller
context propagation and American spellings. No design contradiction found.
All product execution used isolated HOME/SPARKWING_HOME. No live task processes
remain. Scratch was removed at session end. Only this note remains uncommitted.
