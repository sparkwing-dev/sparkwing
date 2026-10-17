# W9 burst artifact, cancellation and profile continuation

Base: `12e936f47`. No Git writes. Retry-selection DESIGN and lead-owned handoffs
and amendments are unchanged.

## Native failures addressed

The personal-machine and contended-machine probes both awarded `consume` but
never started it. The real router/client regression reproduced the cause:
`persisted mask namespace has an invalid job: producer`. The node snapshot had
inserted a partial producer row into Nodes. Mask initialization requires complete
persisted declarations there. The snapshot now carries manifest selectors in
`artifact_sources`, outside Nodes. The client resolves cross-job artifact reads
from that map. The existing generated scope property also verifies that Nodes
contains only the awarded job, with its exact persisted declaration. Undeclared
and nonterminal producers remain unreadable. No producer credential or private
declaration is exposed.

The personal cancellation probe returned while `verify` was still running with
cancellation requested. A scheduler regression reproduced its cause: owner
renewal cancelled the scheduling context on the cancellation beat. During
scheduling, the owner now keeps renewing while the persisted graph cancels and
joins executors, then commits the run verdict. Source preparation still stops
on cancellation. A generated property varies refusal type and renewal ordinal
and verifies that lost authority stops the owner without finishing the run.
Parent cancellation and renewal silence retain the shared keeper behavior.

Both regressions ran red before their fixes and pass afterward. Their commands
are the nodehost cancellation test and controller artifact test in the evidence
below. Native verdicts still belong to the lead harness.

## Profile construction and publication

The previous session's scoped profile credential and publication implementations
are retained. A new real-router/store/signed-filesystem test reads verified
default-branch output, manifest and artifact bytes through an ordinary account
profile after publication. It also checks credential cleanup. The verified cache
record is seeded with fixture App evidence; this does not prove GitHub webhook
verification.

That test exposed a product bug: the remote cache rejected a zero optional
deadline, while native local hosting has no additional lookup deadline. The
reader now retains the caller's context and cancellation unless an explicit
Budget deadline narrows it. No timeout or limit was added. Cancellation and
failed-byte verification tests also run without an extra deadline.

The existing public native test family now covers attached and detached
publication, plus attached and detached profile-cache restoration before the
first SDK step. The cache fixture produces real bytes through Cloud-owned
execution, then supplies fixture default-branch evidence. Detached publication
waits on the existing finish operation through a test barrier. Every added CLI
invocation captures stderr with CombinedOutput and prints it on failure.
No test-only product flag or route was added.

## Handoff audit and test corrections

The existing A1-09/16/17/38/41/43/49/51/52 implementations and local-integrator
handoffs are retained. A1-50's profile reader and publication now have the
additional execution probes above. A1-53 native evidence remains open.

The W9 authenticated-proxy schedule fixture assumed a fixed argv position for
Git's verb. It failed with `repo URL required`. Its fake now recognizes the
verb independently of the existing Git options. The complete focused CLI
schedule selection passes.

The runner renewal-ownership fixture formerly returned a cancellation beat
without persisting cancellation or implementing its graph. It then hung because
an owner must drain that graph. It now verifies ownership and joined cleanup
through a real authority-loss response. The new nodehost tests cover the
cancellation behavior separately.

The broader controller schedule selection remains red in untouched legacy
fixtures: unresolved numeric GitHub identity in `crons_tick_test.go`, removed
trigger columns in its CreateTrigger helper, and retired grants/routes in
`crons_test.go` and `crons_team_test.go`. The port inventory's W2 schedule section
assigns those surviving intake/lifecycle ports to W1/W3 integration. The final
ScheduleHTTP and local schedule transport tests passed. No removed route or
state API was restored.

## Contracts and volume

C1 records the optional remote-cache deadline semantics. C2/C3 record the
separate artifact-selector map. C3 records owner cancellation draining. The new
private snapshot type replaces partial rows because those rows cannot safely
serve as executable declarations. The scheduling-phase bit keeps cancellation
with the existing graph and lease keeper. Undo requires reverting each binding
with its callers. There is no new service, queue, credential form, route, flag,
environment variable, schema or limit.

Production Go versus HEAD: +42/-21, net **+21**. W9-owned production: +7/-6,
net +1. W9 versus main: +19,011/-23,818, net **-4,807**, within the -2,900
budget. Counts exclude tests, testdata, acceptance and contract declarations.
Burst-named production files are unchanged. This continuation adds only 21 net
production lines to the existing burst/profile implementation. Deleted code is
the partial producer-row projection and its client scan, plus the zero-deadline
refusal and replaced owner composition lines.

Shared production files: `internal/blob/remote.go`,
`internal/localhostwire/private_host.go`, `internal/nodehost/owner.go`,
`pkg/controller/private_host.go`, and `internal/apispec/wire.go`.
W9's `pkg/controller/client/private_host.go` consumes the new private shape.
Shared tests: blob remote verification, nodehost owner lifecycle, runner renewal
ownership, controller artifact projection and profile publication. Source docs,
contracts, generated OpenAPI and embedded mirrors changed with the behavior.
No API removal required additional foreign callers to migrate.

## Evidence

Go commands use `GOWORK=off GOFLAGS=-buildvcs=false GOCACHE=/tmp/sw-gocache`,
`TMPDIR=/tmp/sw-astra-W9`, `GOTMPDIR=/tmp/sw-astra-W9/go`, and at most `-p 4`.

The focused regressions use these selections from the worktree:

```sh
go test -p 4 ./pkg/controller -run 'Test(ClaimedHostReadsOnlyDeclaredProducerManifestSelectors|ProfilePublicationImportsRecordsAndLogsWithoutClaims)' -count=1
go test -p 4 -race ./internal/nodehost -run '^TestOwnerHost(AuthorityLoss|Cancellation)' -count=1
```

Passed: whole-repository build; focused vet; full short blob/client suites;
API-schema and controllerwire checks; real controller profile/publication,
artifact-selector and native-data selections; CLI burst/local-run/schedule
selections; shared native-host and cache selections; storage remote-cache and
publication component acceptance; focused controller and owner race checks;
commentcheck against main; sleepcheck against HEAD; full doccheck; mirror and
whitespace checks. OpenAPI's first drift failure passed after regeneration.

No archived test was newly ported and no native acceptance cell is closed.
Socket-dependent public CLI and HTTP SDK probes compile and skip at this
sandbox's socket refusal. The lead must run its existing native commands with
no skips; `^TestBurstPublicRun` includes the new profile cases. The latest native
file still contains FAIL for personal execution, contention and cancellation.

Real integrations left open are the lead's native rerun, deployed Cloud/DinD
and object-network execution, and the existing native Darwin/Windows/PostgreSQL
lanes. The known requester-secret restart-loss residual is unchanged. Friction:
socket refusal, stale legacy controller cron fixtures and Overwing's reported
source lag. No installation, deployment, release or operational state changed.
All checks are joined before scratch removal. `/tmp/sw-astra-W9` is removed at
session end; `/tmp/sw-gocache` is retained.

STATUS: CONTINUE
