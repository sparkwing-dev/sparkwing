# W9 full-area review fixes

Session base: `bad704740`. No Git writes. Lead-owned amendments and handoffs
are unchanged. The three remaining implementation items are connected; native
verification remains with the lead harness.

Implemented:

- Ruling 5: trigger and Cloud overflow capture present, allow-listed environment
  variables through procenv. Empty values and native bytes survive transport.
  Engine-owned and credential-shaped names are excluded. Values of at least
  12 bytes and embedded proxy credentials are masked before inspection or code.
- Intake validates the names/digest and allow-list and stores values encrypted
  in `runs.submitted_env`, outside public request/spec JSON. Encryption binds
  team, execution ID and purpose. Replay cannot change values. Retry and child
  intake retain them and rebind encryption to the new execution. Run deletion
  removes the private column with the row.
- Live direct awards carry submitted values, including fresh node awards and
  successful bootstrap exchange. Bootstrap/source awards, launcher sync and
  reconciliation without a fresh bearer carry none. Release rechecks tenant,
  authority ancestry, repository binding, cancellation and lease. Host
  construction verifies the immutable environment before source installation,
  planning, attempts and evaluation. Missing or altered values fail closed.
  Server plan acceptance also refuses identifiers containing registered values.
- Ruling 7: `--sw-allow-unadmitted` selects decision 91's exclusive in-process
  controller. It uses the same election, startup reconciliation, admission,
  concurrency, workers and joined shutdown. It refuses detached or Cloud-routed
  use. A competing owner is neither opened nor stopped. CI should supply a
  private SPARKWING_HOME. The flag avoids the shared machine admission queue;
  it does not disable the standalone controller's own admission rules.
- The real help test no longer expects retired `--target`, and asserts that
  help does not advertise it. Korey's approval covers this cut and profiles
  `--token`; the earlier implementations remain intact.

Production accounting: +348/-22, net +326 across all packages this session.
W9 alone: +87/-15, net +72. W9 versus main: +19,569/-25,168, net -5,599,
within its -2,900 budget. Counts include new production files and exclude tests,
testdata and contract declarations. No whole production file was deleted.
Deleted the submitted-environment refusal, standalone refusal and redundant
identity-only trigger environment input. No compatibility shim was added.

The environment invariant is that only present, allow-listed, noncredential
variables reach remote processes; private values stay outside public views and
are masked before execution. The claim invariant is that only a live direct
execution award receives its run's values. The standalone invariant is that one
process owns a home's store and joins its own shutdown without disturbing a
competing owner.

Tests cover real parsing, SDK inspection, controller HTTP intake and claims,
node reconciliation, Cloud exchange, controller restart, every remote process
kind, sealed identity validation, empty/native-byte values, duplicate process names, encrypted retry,
corrupt/transposed/missing ciphertext and atomic invalid-intake refusal.
Property tests generate environment values and check filtering, masking,
plaintext absence and tenant/claim fences. The procenv fuzz run executed 1,243
cases; the store fuzz run executed four expensive database cases. Both passed.
Native burst tests now require a submitted value in the actual here/personal
job and require masked log output. A new native standalone test checks current
process ownership, competing-owner refusal, real pipeline execution and joined
shutdown. Those socket-dependent probes skip in this sandbox; they are not
claimed as native runtime proof.

| Option | Kept or cut | Source and state |
| --- | --- | --- |
| Relative --sw-priority | Kept | C4; local and Cloud queue resolution implemented earlier. |
| --sw-no-bincache | Kept | C4; source policy retained through inspection, hosting and retry. |
| --sw-hash-all-files | Kept | C4; source policy retained through inspection, hosting and retry. |
| --sw-allow-unadmitted | Kept | C4 and decision 91; exclusive in-process CI controller implemented. |
| --sw-index | Kept locally | C4; private self-contained captured index implemented earlier. |
| --target | Cut, approved | v0.6.0 pipeline-name selection; Korey approved removal. Help test corrected. |
| --sw-request-id | Kept | C4 detached tracing; implemented earlier. |
| Hooks --fleet and --all | Cut earlier | Ruling 4; use --all-repos. |
| profiles --token | Cut, approved | Ruling 12 and Korey's approval; use --token-stdin. |
| profiles add --scope | Cut earlier | Simplification 5; member credentials. |

Contract changes made under Integration phase authority:

- C1 records the encrypted private run column, cipher configuration, release
  fence and inheritance through the sole intake transaction.
- C3 records the two private `SubmittedEnv map[string][]byte` wire fields,
  direct-award/exchange delivery and bootstrap/sync exclusions.
- C4 records capture, verification, remote base composition and standalone
  lifecycle semantics. No route, grant, setting, environment name or limit added.

Mechanism added: encrypted run-bound storage is necessary because remote hosts,
controller replicas and retries cannot read a submitter's private local files.
A volatile process map would lose values across controller replacement. It uses
existing row-bound credential encryption and the run's existing retention.
Removing the private column, capture and award binding would restore the prior
explicit submission refusal. Standalone uses the existing runtime rather than
adding an admission implementation.

Shared production files touched: `internal/procenv/{build,submitted}.go`,
`internal/runspec/environment.go`, `internal/wingd/localcontroller/native.go`,
`internal/cluster/production_runtime.go`, `internal/store/{store,run_spec,
plan_contracts,submitted_environment}.go`, `internal/store/schema.sql`, and
`internal/controller/{server,engine,engine_claim,engine_intake}.go`. Their focused
callers/tests were migrated. W9 callers changed in `trigger_prepare.go`,
`trigger_native.go`, `burst_native.go`, `local_run_host.go` and standalone run
composition. Generated CLI/API references, OpenAPI schema and changelog mirrors were
regenerated from their owners, including `api/openapi.yaml`. Its schema also
refreshes the retained source build-policy fields from the previous session.
No unrelated API caller was removed.

Verification passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9 and
GOTMPDIR=/tmp/sw-astra-W9/go, using `go -C` and at most four package workers:

- Whole-repository `go build -p 4 ./...`.
- `go vet -p 4` on cmd/sparkwing, procenv, runspec, cluster, localcontroller,
  controller and store.
- Focused cmd/sparkwing tests with rewrite_integration: native trigger, trigger
  preparation, parsing, local run, help, burst preparation/public probes and
  standalone. Socket probes skip; the remaining checks pass.
- Controller/store Engine, Admission, Plan, LocalSecrets, Retry, SubmittedEnvironment
  and RequesterLocalSecret tests and fuzz seeds.
- procenv, runspec and controller/client focused boundary tests; cluster
  Production/Runner tests; localcontroller TestNativeServices tests.
- Existing TestSurface* and TestEngineAmendments* acceptance cells. No cell was
  newly unskipped and no archived test was newly ported.
- Actual procenv/store fuzz runs with two workers; commentcheck against main;
  sleepcheck against HEAD; doccheck; OpenAPI shape drift check; documentation
  mirror check; git diff --check.

Remaining: the lead must commit/review and run the native harness. Its latest
W9-burst-native-latest.md still contains FAIL from the previous state, including
TestRunHelpListsArcFlags and the stale internal/projectconfig package target.
The correct package is pkg/projectconfig. No harness report was edited.
Native standalone and burst execution, deployed Cloud/DinD, PostgreSQL,
Windows/Darwin and A1-53 remain the lead's real-integration evidence. No foreign
stub or further contract change blocks this implementation. Earlier amendment
and handoff work remains completed; no new W9 assignment appeared.

Friction: native sockets are denied by the sandbox; Overwing reports behind
source. During final rechecking, shared-cache package files disappeared and
caused a store test compilation failure. A subsequent build recreated a named
missing file. The cache was kept; affected checks were rerun after rebuilding. Go test fixtures and generated docs use a temporary test driver rather
than invoking a Sparkwing binary. The temporary driver was removed. No Git
identity or core.worktree setting was written. All checks joined, no scratch executables remained, and W9 scratch was deleted.
The shared build cache is kept.

STATUS: CONTINUE
