# W9 claimed-run hello labels

`engineReconciledClaim` left `Runner.Labels` nil. The run host carries that
identity through `NewRunInput` into the SDK hello. `Request.Validate` rejects
nil labels with `bad_request` before writing the request. The planner reports
any failure before secret discovery as `describe failed`, so the error appeared
to come from describe. This also explains the absence of a controller error.

Both describe calls contain only their correlation ID and `op: describe`.
Neither sends arguments, a run context, a pipeline name or a protocol field.
Direct inspection's hello supplies an empty labels array. Claimed execution's
hello supplied nil, which would encode as `runner.labels: null`. Registration
`example`, type identity `Example` and the empty input map are unchanged.

The shared award constructor now supplies `Labels: []string{}`. This covers
claim, reconciliation and launcher synchronization without relaxing protocol
validation or changing diagnostic masking. The protocol guide and its embedded
mirror now state the required empty-array representation.

`TestClaimedRunDescribesRepositoryExampleOverSDKPipes` builds this checkout's
actual `.sparkwing` program. Plain describe first confirms that `example`
declares an empty arguments array. The test then obtains an HTTP-issued award
through the real controller and store, projects it through `NewRunInput`, and
calls `SDKPlanner.Prepare` using real SDK processes and protocol pipes. It also
runs the fresh plan process and requires the example's `configure` job. Only
engine resolution is supplied as an adapter. Test-only fixture exports avoid
the controller-to-nodehost import cycle.

The regression ran red with the exact reported diagnostic and passed after
changing only the labels initialization. The existing reconciliation test
also requires a non-nil labels array.

Verification used `GOWORK=off`, `GOFLAGS=-buildvcs=false`,
`GOCACHE=/tmp/sw-gocache` and `GOTMPDIR=/tmp/sw-astra-W9/gotmp`.

- Root and `.sparkwing`: `go build ./...` and `go vet ./...` pass.
- The new SDK regression, reconciliation projection, HTTP claim tests and
  launcher synchronization tests pass.
- Native local preparation, the replacement-SDK pipe regression and the
  nodehost SDK-planning tests pass.
- Commentcheck against main, documentation mirror drift and whitespace checks
  pass.

An additional controller test selection fails in
`TestEngineBootstrapExchangeReturnsOriginalUnitOnce/plan` and `/node` at
`engine_amendment_test.go:201` with `claimant repository scope excludes work`.
Both failures also occur with the original production file supplied through a
Go overlay. They happen during store claiming before award projection and were
left unchanged.

The rebuilt CLI was run from this checkout with an isolated HOME and state
under `/tmp/sw-astra-W9/home`. `sparkwing run example -o plain` reaches local
controller startup and stops at `dial unix .../d.sock: connect: operation not
permitted`. The sandbox therefore cannot establish the next native product
failure after the repaired handshake. No task-owned Sparkwing or SDK process
remains. Task scratch is removed after checks join; the shared cache is retained.

Shared files touched are `pkg/controller/engine_store.go`,
`engine_store_test.go`, `engine_claim_sdk_unix_test.go` and `export_test.go`.
The other changes are W9 documentation and this report. No foreign contract,
lead-owned handoff, Git metadata, setting, retry or timeout was changed. No
acceptance cell was unskipped and no archived test was ported. No legacy file
was deleted. Production changes are +1/-1, net zero lines. W9-owned production
versus main is +16,916/-23,812, net -6,896 lines.

Friction: Xwing environment inspection cannot write its session-index lock in
this sandbox, so the candidate was built directly under task scratch. The
isolated HOME also hides the asdf Go configuration; the CLI exercise used the
installed Go toolchain's absolute bin directory in PATH. No new mechanism or
open contract question remains for this fix.
