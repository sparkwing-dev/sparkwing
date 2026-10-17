# W3 native output and spark resolution continuation

The lead's latest native evidence has two failures. The prior missing-socket and
OIDC failures are absent. Install-to-green reaches a zero exit without a terminal
record. The offline test executes a tracking spark without resolving its version.

Foreground local execution now emits `run_start` after intake and `run_finish`
after the host returns its committed terminal outcome. The sealed receipt selects
the format. JSON carries event/attrs records with run ID, pipeline and actual
status. Plain and pretty output carry the same identity and outcome. Failed and
cancelled runs retain nonzero exits. Write failures identify the admitted run or
its committed outcome. The controller queue may continue an admitted run after
a start-record write failure.

Native Go preparation now calls the existing spark resolver before source
installation and intake. The existing `--sw-no-update` flag reaches that call
instead of being rejected as unsupported. It skips resolution while retaining
current module pins and the overlay. Source installation still uses the selected
engine and its existing SDK-pin checks. No controller lifecycle code changed.

## Evidence

Before the fix, the foreground regression failed for success, failed and
cancelled outcomes because no completion was emitted. The no-update regression
failed at flag validation. Native preparation's tracking-spark regression reached
source acquisition instead of returning the resolver's offline error.

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOMAXPROCS=4,
GOCACHE=/tmp/sw-gocache, temporary paths under /tmp/sw-astra-W3 and at most four
package workers:

- Whole-repository build and focused vet of the CLI and SDK installer.
- Focused local-run, preparation, output-selection and writer-failure tests.
- The full selected-engine SDK installer suite, including effective overlays,
  SDK replacement refusal and execution after cache pruning.
- The install-to-green stub contract checks for accepted commands, phase
  reporting and refusal to call an unproven run green.
- Commentcheck against main across 2,692 files and whitespace checks.

Independent review found one diagnostic issue with the start-write error. It is
fixed and the reviewer confirmed no remaining finding. The small formatter was
accepted as proportionate. No existing native assertion was weakened or skipped.
No acceptance matrix cell was enabled and no archived test was newly ported.

The native evidence file still contains FAIL. The lead must run the complete
install-to-green and denied-network scenarios against these edits before W3 can
be COMPLETE. The previous session's OIDC changes were not redone.

## DESIGN: cancellation identity across store reads and followers

Invariant: cancelling a state follower after completed work cannot fail the owner,
and independent read or subscription errors must remain observable.

The existing race finding reproduces with `go test -p 4 -race
./internal/nodehost -run '^TestRetryExecutesCommittedSelectionAfterRestart/all$'
-count=20 -timeout=90s`. Three iterations failed with raw SQLite `interrupted (9)`.
A temporary source overlay annotated only the follower snapshot-read error.
Seven of 20 traced iterations failed there with context already cancelled.
Both source completion and retry completion are affected. This is failing race
evidence, separate from the passing checks above.

The two-strikes rule requires a design handoff instead of another follower
special case. Proposal: C1 owns cancellation normalization at its shared state
read boundary. A driver interrupt caused by that read's cancelled context becomes
the standard context cancellation result. Independent and joined failures remain
intact. W3 keeps its existing follower joining and error preservation. Tests must
cover real SQLite interruption and generated combinations of cancellation and
independent failures. No cancellation implementation or foreign store file was
changed. Lead approval and the C1 implementation remain open.

## Contracts, scope and volume

Contract change made under integration authority: C4 records pre-intake spark
resolution, no-update propagation and foreground lifecycle output. No wire,
schema, credential, limit, setting, environment variable, cache or retry was added.
The lead-owned handoffs and amendment files were only read.

Shared files touched: cmd/sparkwing/local_run.go, local_describe_unix_test.go,
run_host.go, run_host_test.go, the new run_output.go, and C4 surface/configuration.
The native preparation signature's runtime caller and direct test caller were
migrated. No API was removed. Production changes are +44/-5, net +39, all in
shared CLI files. The five replaced production lines include the unsupported
flag refusal. No legacy module was removed in this session. W3-owned production
remains +11,790/-26,047 versus main, net -14,257 under the lead's exclusions.

Friction: native socket evidence belongs to the lead harness, the separate
SQLite cancellation race remains reproducible, and installed Overwing reports
that it trails its source by 10 commits. No Git writes, installs, operational
Sparkwing executions or releases ran. Scratch and the tracing overlay were
removed after all checks joined. The shared build cache was retained.

STATUS: CONTINUE
