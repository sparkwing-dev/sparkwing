# W9 burst local fallback and retained overflow usage

Starting HEAD: `8a95049fe`. Earlier burst routing, source, requester secrets,
contention, personal machines and native cache composition are retained.
No Git writes, merge, install, deployment or operational Sparkwing command ran.
Lead-owned amendments and handoffs are unchanged.

## Implemented

- Reproduced local fallback dropping configured requester secrets. Native local
  preparation now resolves the selected pipeline's declarations against the
  captured requesting environment. It carries values through the existing
  private intake map and seals only their names. Local routing uses the same
  path. The local controller's existing volatile secret registry and masked
  release transport are reused.
- Moved local environment capture after configured secret entries are removed.
  SDK inspection uses that filtered environment. Ordinary local environment
  entries remain available. Preparation and intake errors register the private
  values for masking, including structured errors.
- Reproduced monthly overflow usage changing from 70 seconds to zero after
  deleting its run. Billing awards now retain the requesting account, derived
  from the run's authenticated account creator at award time. Usage reads the
  retained financial records. Deleting or reusing a run ID cannot erase the
  usage or attribute it to a replacement run's creator. Machine/account ID
  collisions retain distinct attribution.
- Added a public CLI offline-fallback execution probe covering requester-secret
  masking, edited source, artifacts, declared cache restoration and local run
  history. It compiles but skips at this sandbox's socket boundary.
- Updated source docs, embedded mirrors and the changelog. Tagged the existing
  PostgreSQL fixture-clock comment in the already-touched store file so
  commentcheck against main passes.
- Removed the stale `expireSourceUploadsTx` SQL-scope exemption. Its source
  statements already carry the tenant key, so the guard now checks them.

Invariants: configured requester-secret environment entries never enter saved
local environment snapshots. Intake diagnostics never expose their values.
Run deletion and identity reuse never erase or transfer account overflow usage.
Generated tests exercise encoded snapshot files, opaque/structured errors,
account/machine collisions and replacement run creators through real files,
native SDK inspection and the store.

## Contract changes made

- C1: `billing_awards.requester_account` retains account attribution independently
  of run/account retention. Non-account creators retain an empty value. The
  rewrite baseline advances from 5 to 6. Version-5 stores are refused and need
  a fresh database. No migration, backfill or extra accounting table is added.
- C4: local preparation and offline fallback use the configured requester-secret
  resolver and private intake map. Selected entries are filtered before local
  environment persistence and SDK inspection.

No new wire field, flag, environment variable, cap, service or credential path
is added. No additional foreign contract change is requested for these fixes.

## Verification

Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package/fuzz workers.

Passed:

- Whole-repository `go build -p 4 ./...` and focused vet on CLI, store,
  controller/client, nodehost and localcontroller.
- Store burst, overflow, requester-secret, metered-award and schema selections.
- Complete short store suite, 235 seconds. The previously failing standalone
  ledger selections and tenant SQL-scope guards pass.
- Controller burst intake, ranked claims, requester-secret release, overflow,
  SDK process execution and fake-Kubernetes Cloud Job selections. The compiled
  uploaded SDK executes here, on a personal machine and after Cloud exchange.
  Source installation uses the selected engine. FIFOs replace SDK HTTP sockets.
- CLI burst preparation/source/routing, local preparation/hosting and private
  diagnostics selections. Native local preparation compiles and inspects the
  actual SDK. Alias and ordinary local-environment behavior remain covered.
- Race checks on retained overflow usage and the local secret/diagnostic fixes.
- Three-second fuzz campaigns: retained attribution, 8 executions; native local
  snapshot exclusion, 2 executions; private intake diagnostics, 3,991 executions.
  Snapshot seeds compile and inspect the SDK, so this campaign is small.
- W9 surface grammar, query/prune, management authority, wire detail, run
  observation, enrollment and credential-policy acceptance selections.
- Embedded docs excluding listener-based web-client tests, full doccheck,
  mirror drift, whitespace and `commentcheck -base main` across 2,724 files.

No archived test was newly ported. No native matrix cell is newly unskipped.
The first full short store run exposed ledger-only tests that create financial
awards without run records. Account projection now preserves that independent
ledger behavior with empty attribution when no run exists. The same run found
the stale SQL exemption. The focused failure selections and final full short
store recheck pass after correction.

## Remaining integration

All six public CLI execution cases skip at `listen tcp 127.0.0.1:0: socket:
operation not permitted`. The cancellation parent reports PASS with skipped
children. These skips do not establish end-to-end completion.

The lead should run outside this sandbox:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m

env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./pkg/controller \
  -run '^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$' -count=1 -v
```

Real integration points remain public CLI/controller startup, terminal output,
Ctrl-C, competing machine admission, personal-runner lifecycle and cache/artifact
transfers through native sockets. The development personal-runner fixture
prestarts its controller because its replacement-pin engine is the test
executable. Fresh-machine startup with the signed released CLI still needs its
native lane; no engine verification was weakened to bypass that boundary.
Observation and self-claim remain separate from machine reservation. An awarded
here job waits if competing work takes capacity between those operations.
The deployed Cloud runner image, DinD, object backend and network isolation also
need their real lane. Kubernetes and isolation evidence in the composed proof
are fakes. Native Windows, Darwin and PostgreSQL remain external lanes.

Requester values remain volatile in the supported single-controller topology.
Restart requires resubmission. Multiple replicas need verified affinity or an
ephemeral relay. The prior retry-selection DESIGN remains with the lead and
was not patched again. The inherited A1-50 authorized remote-cache and local
publication bindings are still absent from the production local-run runtime:
`--sw-cache-from` and `--publish` refuse before intake. Native declared caches
within the selected controller use the separate working adapter. This session
does not close those W9 obligations or add fake production bindings.

## Size, deletions and shared files

Production Go this session: +60/-23, net +37. W9-owned production:
+52/-19, net +33. W9 versus main: +18,434/-23,824, net -5,390, within the
-2,900 budget. Counts exclude tests, testdata, acceptance and contract
declarations, using `git diff --no-renames --numstat`. SQL adds one column.

Removed the pre-classification environment capture and the private preparation
API's precomputed-seal argument. Its runtime caller and
`local_describe_unix_test.go` migrate together. No other package called that API.
Earlier legacy subsystem cuts remain; no new legacy subsystem is replaced.

Shared files touched: `pkg/store/burst_overflow.go`, `metered_accounting.go`,
`schema.sql`, `store.go`, `burst_placement_test.go` and new
`burst_overflow_retention_test.go`, plus `tenant_sql_scope_guard_test.go`;
C1/C4 contracts, CHANGELOG and embedded-doc
mirrors. Other edits are W9's CLI implementation, tests and local-execution doc.

Mechanisms changed: one retained account field on the existing financial award,
and reuse of private intake secrets in local preparation. Retaining the run join
loses usage when history is pruned. Keeping a full local environment snapshot
persists the selected secret entries. Separate registries, new transports and
periodic accounting snapshots are unnecessary. Reverting these edits restores
the former join and preparation flow; baseline-5 data is not upgraded.

Friction: forbidden sockets and Overwing's source-lag notice. No approval was
requested. All checks are joined. Worker scratch is removed and the shared Go
cache is retained. Local Git identity/core.worktree overrides are absent.

STATUS: CONTINUE
