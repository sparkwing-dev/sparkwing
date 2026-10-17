# W9 offline native continuation

HEAD: `5f6f781d8`. The four delta-review fixes are already committed. This
session preserves them and investigates the remaining failure in the lead's
`W9-burst-native-latest.md` report from 2026-10-17 09:14 MDT.

That report still fails `TestRun_PinnedPipelineRunsWithTheNetworkDenied`.
The final `--sw-no-update` invocation is admitted, then returns `lock_held`
with `reason=holder_rights` and `state transition refused`. The failing
transition is not identified in the retained output. The cause remains open.

The invariant under investigation is that the awarded owner and job retain
their execution authority through the committed terminal handoff.

Changed `cmd/sparkwing/run_offline_unix_test.go` to print the isolated local
controller's full log when this test fails. The log is read before daemon
cleanup and temporary-directory removal, so the next native failure should
retain the refusing endpoint and its surrounding audit events. Read failures
also appear in test output. Assertions and execution policy are unchanged.

The original test was attempted before this change. It fails earlier in this
sandbox because connecting to the controller's Unix socket returns
`operation not permitted`. That is not a reproduction of the native refusal.
No production correction is claimed without a matching reproduction.

A temporary probe ran 32 iterations through the real in-process HTTP
controller, authentication, store, CLI owner session, scheduler, executor,
step reporting and completion. It used fakes for source acquisition and SDK
replies. All iterations completed successfully. A preceding 32-iteration
probe with a fake executor also passed. Both probes were removed because
neither reproduced the native failure. They do not prove the complete native
services or admission path.

Verification passed with `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache`, `GOMAXPROCS=4`, temporary files beneath
`/tmp/sw-astra-W9`, and at most `go test -p 4`:

- Whole-repository `go build -p 4 ./...`.
- Vet for cmd/sparkwing, clisurface, controllerwire, controller/client and doccheck.
- Focused CLI review regressions and production local-controller admission,
  plus attached-host lifecycle tests: 45.580s. This includes OLD/HEAD source
  preparation, sealing and replan, native overflow entrypoint selection,
  trigger inspection, alias arguments and local alias preparation.
- Real NativeProcessClient child intake with nonempty captured Env and
  adversarial provenance changes: 0.891s.
- Full doccheck, clisurface and controller/client test packages. The docs
  suite validates migration After examples against the current grammar.
- Existing `TestSurface*` and `TestEngineAmendments*` acceptance contract tests.
- Commentcheck against main and diff whitespace.

No acceptance cell was newly unskipped. No archived test was newly ported.
No production code was added or deleted this session. The test adds 17 lines.
W9 production Go versus main remains +19,726/-25,174, net -5,448 lines,
within the -2,900 budget. Existing hard cuts remain intact.

No contract changes were made or requested. No shared implementation file,
lead-owned amendment or handoff file was edited. The four findings' earlier
C1/C3/C4 amendments and W9 assignments remain implemented. No new public
surface or mechanism was added. The report and offline test are the only
changes from HEAD.

The lead's next native run must exercise the offline test and retain its
expanded failure output. Remote split-source execution, deployed Cloud/DinD,
PostgreSQL and native Windows/Darwin remain external integration evidence.
The latest native report still contains FAIL, so W9 cannot claim completion.

Friction: the sandbox blocks the original Unix-socket path. Overwing's
installed binary reports that it trails its source, but standards discovery
worked. No Git metadata, identity or harness memory was written. Checks
joined. Scratch is removed and the shared build cache is retained.

STATUS: CONTINUE
