# W9 burst runner startup continuation

Starting HEAD: `f01cc8239`. Earlier burst implementation is retained. No Git
writes, installation, deployment or release ran. Lead-owned amendments and
handoffs are untouched.

## Implemented

- The real personal-enrollment test now starts its saved configuration through
  the runner parser and credential reader. It failed because startup rejected
  the registered display name written by enrollment. Startup now accepts saved
  display metadata and still refuses explicit holder selection. A generated
  property varies that metadata through the managed runner loop and preserves
  the server-awarded identity and claim selection.
- A real Git bundle regression failed on a missing fresh host workspace.
  Shared source acquisition now prepares that private directory after live
  authorization. The CLI's separate directory creation is deleted. Here,
  registered runners and Cloud hosts use the same acquisition owner. The
  authorization property now covers absent and existing workspaces and proves
  denied access creates no source directory or bytes.
- Added native public probes for personal enrollment, real runner composition,
  source execution with a zero overflow cap, and Ctrl-C during here and personal
  execution. They require masked streamed logs, the admitted handle, the expected
  terminal run/job outcome and no duplicate local run. The personal probe also
  observes the registered machine's actual node award.
- The SDK fixture has a blocking input and cancellation marker. Process waits
  use channels and deadlines, and cleanup joins the runner. New helper-entrypoint
  regressions execute successfully without listeners.

Invariants: saved registration metadata cannot replace server-awarded execution
identity. Denied source access cannot create a workspace or acquire source bytes.
The runner and source properties exercise those invariants with adversarial inputs.

## Contract changes made

- C4 records saved runner display metadata versus explicit holder selection,
  and moves fresh workspace preparation from CLI options to shared acquisition.
- C3 protocol/SDK records source authorization before directory preparation and
  acquisition-owned checkout cleanup.
- C3 controller wire records the new native personal and interruption probes and
  their verification limits. No runtime wire field, grant, schema, environment
  variable, default or cap changed.

Run docs, machine settings, changelog and generated embedded mirrors are updated.
No foreign contract change is requested. Existing retry-selection DESIGN remains
with the lead as recorded in W9-BURST-FINALIZATION.md.

## Verification

All Go commands used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and
TMPDIR=/tmp/sw-astra-W9, with at most four package or fuzz workers.

Passed:

- Whole-repository `go build ./...`; vet on cmd/sparkwing, internal/source and
  internal/cluster.
- Complete source and cluster suites. Focused CLI burst/source, enrollment,
  runner preferences and here attachment/resume tests.
- Real uploaded edited SDK preparation, requester environment seeds and
  enrollment-to-startup regression.
- Controller burst acceptance, including in-process and native-pipe SDK
  execution through here, personal and exchanged Cloud awards.
- Race checks on fresh bundle acquisition, source authorization, registered
  runner identity, real personal enrollment and uploaded SDK preparation.
- Three-second fuzz campaigns: runner identity 4,831 executions; source
  authorization 8,732 executions.
- Clisurface, controllerwire, controller/client, doccheck and embedded-doc test
  selections. Doccheck executable and embedded mirror/changelog drift checks.
- Existing surface grammar, run-query/prune transport and credential-policy
  acceptance selections. No matrix cell was newly unskipped and no archived
  test was newly ported.
- Expanded CLI/runner helper entrypoints. Commentcheck against HEAD across ten
  Go files and diff whitespace.

Commentcheck against main reports the unchanged pinnedClock comment at
internal/store/store.go:108. The native public probes compile but skip on the explicit
sandbox loopback-listener refusal. No native execution or whole-repository test
pass is claimed. An initial doccheck invocation omitted its required directory
arguments. The corrected invocation passed all checks.

## Native handoff and remaining integration

The lead should run, outside the socket-restricted sandbox:

```bash
env GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache \
  go -C /home/bombuntu/.xwing/envs/1087/sparkwing test -p 4 \
  -tags rewrite_integration ./cmd/sparkwing -run '^TestBurstPublicRun' \
  -count=1 -v -timeout=15m
```

Also retain TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards in
internal/controller from the preceding handoff.

The personal public fixture uses the same development test executable for CLI
and production runner entrypoints because local SDK replacements bind the
installed executable's digest. It shares the requesting machine's admission
daemon. This does not prove separate release executables, signed engine download,
cross-machine SDK replacement portability or independent runner admission startup.
The original here probe still uses the built public CLI.

Native personal execution and cancellation remain unverified until those probes
run. Full launch proof still needs the real Cloud Job path, artifact retrieval
and competing local admission through the public CLI. Existing component proofs
for all five requested burst features remain. One-controller ephemeral-secret
delivery retains the preceding documented topology and restart boundary.

## Size, shared files and friction

Production Go this session: +5/-7, net -2. The removed lines are the duplicate
CLI workspace creation/import and the replaced startup condition. No API or
legacy subsystem was replaced this session. Earlier cuts stand.
W9 versus main: +18,344/-23,824, net -5,480, within the -2,900 budget.

Shared implementation/test files touched:

- internal/cluster/runner_cli.go and runner_runtime_test.go.
- internal/source/checkout.go, authorization_test.go and fresh_workspace_test.go.
- C3 controller-wire/protocol-SDK and C4 contract docs, CHANGELOG.md and
  generated pkg/docs mirrors.

Other changes are W9 CLI/docs files. No removed API required caller migration.
Mechanisms added: test-only runner entrypoint and native observation helpers.
Production uses existing parsing, credentials, source acquisition and private
directory helpers. No additional runtime mechanism or user restriction was added.

Friction: sandbox listener refusal, the local-replacement executable identity
boundary, inherited main commentcheck finding and Overwing's reported source lag.
Checks were joined before cleanup. Worker scratch is removed at the end, and
the shared Go cache is retained. Local Git identity/core.worktree overrides are
absent.

STATUS: CONTINUE
