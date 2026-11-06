# Local runner migration progress

Step 3 remains incomplete. Do not land or push. Keep this note uncommitted.

New commits: 67aa0f011 extracts runners.NativeHosting and reuses it in cluster and
foreground local/here hosts; 5e01be2de updates ownership docs. Private awards,
sealed environment snapshots, injected bundle transport, prepared-source roles,
requester-secret copying and HTTP state reads have parity tests. Cloud Docker and
Actions policy stay in cluster. No SDK, schema or wire changes.

Full runners, cluster, CLI (282.379s) and .sparkwing/jobs (177.162s) suites passed.
Final focused checks on repository Go 1.26.10 passed: common-host tests, tagged
owner/node/plan/eval and private-source binding, real foreground generators,
doccheck, vet, lint/depguard, formatting, comment/sleep checks, actual source-doc
checks and docs sync. Code and docs received independent review. The first full
suites used Go 1.27; its mismatch with the Go 1.26-built linter caused a panic,
resolved by selecting the cached repository toolchain. No source workaround.

Existing opt-in integration failures were reproduced through Go overlays of the
original composition at 643e98ea7: cluster TestProductionRunnerCommandsClaimAndHost
(claim/host probe, missing mask registration), Cloud exchange/installed
run-node (missing Docker setup), eligible scheduling (missing mask registration),
and CLI TestRunAnywhereRunnerHostsPlanEvaluationAndJobs (sealed here machine []).
They remain deferred. Foreground generator execution passes with a temporary
private secrets key supplied through the existing key-file setting. Raw logs
are retained in ignored .sparkwing/cache/localrunner-http-hosts/. Scratch removed.

Next: port local cache/publication into the common host with tests, then remove
embedded direct-store host adapters and claimant. Add durable runner instance
PID/start/version and atomic activation/drain claim gating; supervise detached
OS services; move foreground/here hosting and terminal observation to the runner;
prove controller restart/roll and native service survival. The embedded queue and
local_host.go direct bindings remain. Source uses localAwardSourceOptions and
hostSourceOptions to preserve Base.Local, captured snapshots and LocalOnly engines.

Foreground generation-zero local_foreground triggers need a named claim by their
creator. Remote here retains its run-bound bearer and selected transport. Transfer
captured requester secrets, never the detached runner's ambient values. Retain
common-loop host accounting, and let new instances execute dependencies of
waiting owners in a draining instance.

Prior verified work: 3252fbd5e claim retry/Loop.Drain; 652aedc23 exit timestamps;
ce2f36095 docs; 558d50b1f and 2cf25dd97 orphan cleanup/capacity/cancel tests;
5fa90b2d9 migration guidance. The lead gate passed those earlier commits.
