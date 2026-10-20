# W9 detached profile-cache construction

Base: `bd0c83b08`. No Git writes. Retry selection and the lead-owned amendments
and handoffs are unchanged.

The latest native evidence passes edited-source execution, personal placement,
overflow, local fallback, both publication modes, attached profile-cache reads,
cancellation and all three SDK HTTP placements. Detached profile-cache execution
remains pending until log following times out. That file still contains FAIL.

This session reproduces a product bug in that path. The real local intake clears
requester-supplied `Git.DefaultBranch`. The queued host then passed that cleared
field to a cache constructor which required it. Attached execution had constructed
its cache before intake, so it escaped the bug.

The cache constructor now reads origin's default-branch hint from the sealed
local checkout. Its repository identity still comes from the admitted request.
It reuses the burst branch reader, the existing Git process environment builder
and the trigger checkout reader. The shared reader ignores inherited repository
overrides. No trust fact is added to the execution seal. Remote lookup still
requires the producing record's verified default-branch evidence.

Invariant: profile cache construction uses its authorized checkout and sealed
repository, never an inherited Git repository override, and grants only verified
default-branch reads. A generated property checks 32 inherited override paths.
The real-intake regression checks that default-branch metadata was cleared,
constructs the correctly scoped profile reader through a transport fake, and
checks revocation after cancellation. Both tests ran red before their fixes.
The construction failure was `run: cache profile requires origin's default branch`.
Both now pass, including under the race detector. This proves construction;
the native rerun must establish whether the observed stall is resolved.

Profile publication retains its existing implementation. Both publication modes
pass in the authoritative native file. Real-router/store profile credential and
publication tests also pass here. Public burst subprocess failures already print
CombinedOutput, including CLI stderr; the fallback status probe retains separate
stderr. The native log timeout was a killed process, not a bare exit-status error.

The remote-cache acceptance fixture also failed. Its purported verified record
had neither App delivery nor SHA evidence, contrary to C1's Record.VerifiedDefault
contract. The fixture now includes both. New missing-delivery and missing-SHA
cases verify that production continues to refuse incomplete evidence. No
production authorization was relaxed to make this test pass.

Verification uses GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, TMPDIR=/tmp/sw-astra-W9,
GOTMPDIR=/tmp/sw-astra-W9/go and at most four Go workers per command:

- Whole-repository `go build -p 4 ./...` passes.
- Vet passes for cmd/sparkwing, controller/client, docs and acceptance.
- CLI burst, local run/owner/cache/publication and queue selections pass.
- Profile credential and publication controller tests pass.
- Full short controller/client and doccheck suites pass.
- `TestSurface*` and `TestDataRemoteCacheForcesVerifiedDefaultLookup` pass.
- The new profile regressions and original-branch snapshot test pass with -race.
- The embedded docs mirror guard, commentcheck against main, sleepcheck against
  HEAD and diff whitespace check pass.

A broader docs selection hit TestWebClient_DocVersioned's forbidden socket.
That web test did not execute its assertions. Its relevant socket-free mirror
guard passes. Native CLI/socket, deployed Cloud/DinD, cross-machine transfers,
platform and database lanes remain real integration points for the lead.
The lead should rerun `^TestBurstPublicRun` in cmd/sparkwing and
`^TestBurstNativeSDKHTTPExecutesHerePersonalAndCloudAwards$` in internal/controller,
both with rewrite_integration. No native cell is promoted by component results.

Contract changes made: C4 records post-intake branch lookup and the retained
server verification. Contract changes needed: none. No new flag, route, setting,
environment variable, service, schema, public API or DESIGN change was added.
No archived test was newly ported. No legacy subsystem was replaced this session.

Production Go this session: **+21/-8, net +13**. Eight former production lines
were replaced; whole legacy files deleted: **0**. The existing burst/profile
implementation grows by 13 lines. W9-owned production versus main is
**+19,026/-23,818, net -4,792**, within the -2,900 area budget.

Shared file touched: tests/acceptance/data_binding_test.go. Caller migration is
limited to cmd/sparkwing/local_host.go, local_run.go, burst_native.go and
burst_source_test.go. The remaining files are burst_source.go, the new
local_profile_test.go, C4, caching.md and its mirror, and W9's report/handoff.
No foreign production package changed.

Friction: sandbox socket denial and Overwing's source-lag notice. All subprocesses
have joined. Scratch is deleted at session end; the shared build cache is kept.
No unresolved contract decision was introduced. Native completion remains open.

STATUS: CONTINUE
