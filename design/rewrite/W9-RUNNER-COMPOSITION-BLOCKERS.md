# W9 runner composition continuation

Base `79092b50a`, environment `1087`. Runner composition remains incomplete.
No production code changed because the remaining pieces require foreign
contracts that the swarm brief forbids W9 from changing without a handoff.

## Added evidence

`TestProductionRunnerCommandsClaimAndHost` now drives both `start` and `warm`
through the real config parser, private credential file, production factory,
managed runner, mixed trigger/node claim loop, independent award renewal and
native binding. Only external HTTP is scripted. No controller or host factory
is substituted. Enrollment authority is checked on claims, and award authority
is checked on renewal and every private request. The production development
build policy is used to reach claims without replacing the policy resolver.
Released-build policy enforcement remains covered by the installed-entrypoint
probe.

Both commands reach the native TCP listener for node awards, where the sandbox
returns EPERM. These cases skip explicitly. Both commands fail before sockets
for trigger awards with `owner hosting requires canonical placement eligibility`.
The start probe cancels at the next claim after the first host joins. The warm
probe uses its existing one-claim limit. No sleeps or timing overrides were added.
The fixture now carries the persisted owner, plan hash, job status/hash and
explicit runner labels needed after listener construction. Unexpected private
requests return errors instead of terminating a worker goroutine with Fatal.

## Contract changes needed and real integration left open

1. **W1/W2/W3 owner placement.** Invariant: an owner self-executes only when the
   canonical matcher admits its live enrolled profile and sealed job demand.
   `NativeConfig.Local` has no production projection. `Store.ReadClaimProfile`
   exposes a profile, but canonical demand construction remains private in
   `readMatchingInputsTx`. The private host wire carries neither that complete
   input nor an eligibility result. The lead must assign a canonical read
   contract and its authenticated transport. Do not duplicate the demand builder
   in W9. Keep the transactional matcher check on self-claim.
2. **DESIGN: W1/W2/W3 owner/eval reporting.** Invariant: evaluation reports use
   their live owner/eval lease and immutable source fence, without creating a
   work attempt. The existing real-controller owner annotation and summary
   probes still return HTTP 409, `reason=holder_rights`.
   `checkResultWriterTx` requires a live node holder and positive attempt.
   Eval's fixed grants omit NodeReport, which masks/cleanup require. Latest
   references also retain a node-claim-only parent relation. This is the existing
   two-strikes DESIGN request, not a new special-case patch. C1/C2 must define
   the committing evaluation-source proof and service grants before W9 binds it.
3. **W8 production Cloud boundary.** `ProductionRuntime.Launcher` is still nil.
   `KubernetesBoundary.Probe` and `Clear` remain required callbacks without
   production implementations. W9 needs their constructor or concrete callbacks
   proving network enforcement and reservation-scoped cleanup. Wiring nil or
   no-op callbacks would move the failure without implementing isolation.
4. **W3/W8 Cloud plan/eval hosting.** Bootstrap exchanges both awards once, but
   production `runners.Hosting` rejects them before renewal because it supports
   trigger and node only. W9 needs the shared plan/eval unit-host implementation,
   private-state authentication and lease renewal. Broadening the validator alone
   would send those units through trigger-owner execution.

Numeric selectors are already implemented. The current real-controller read,
receipt, debug, resume and cancel regressions pass. All 24 metadata authority
property seeds and 14 debug team-isolation seeds pass. No numeric-selector
production change was needed.

## Verification

Passed: repository `go build -p 4 ./...`; tagged vet of `internal/cluster` and
`cmd/sparkwing-runner`; their full short suites; numeric-selector tests and
property seeds; commentcheck against main across 2,573 files; whitespace check.
The final tagged composition selection reproduces all four gaps above. Start,
warm, source fetch and installed run-node still reach their production HTTP
operations. Cloud node exchange reaches awarded renewal. Owner metadata remains
red independently of the sandbox. Native pipeline execution remains with the lead.

Reproduce using GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/gotmp and
TMPDIR=/tmp/sw-astra-W9, from this worktree:

```sh
go test -p 4 -tags rewrite_integration ./internal/cluster -run 'TestProductionRunnerCommandsClaimAndHost|TestProductionRunnerOwnerReachesNativeHost|TestProductionRunnerNodeReachesNativeHost|TestProductionCloudExchangeReachesAwardRenewal|TestInstalledRunnerRuntimeComposition' -count=1 -v
go test -p 4 -tags rewrite_integration ./internal/controller -run '^TestReviewOwnerEvaluationReportsBeforeWorkAward$' -count=1 -v
go test -p 4 ./internal/controller -run 'TestPublicNumericSelectors|TestDebugNumericSelectors|FuzzDebugSelectorsStayWithinAuthenticatedTeam|Fuzz.*Numeric' -count=1
```

No native acceptance cell was newly unskipped. No archived test was ported.
Production additions/deletions are **0/0**. No legacy code or caller was removed.
W9-owned production Go versus main remains **+16,945/-23,817, net -6,872**,
within the -2,900 budget. Mechanisms added: none.

The only shared file touched is
`internal/cluster/production_runtime_integration_test.go`. This report is the
other changed file. Lead-owned handoffs/amendments and foreign contracts are
unchanged. No Git write, candidate installation, operational Sparkwing command,
pipeline, deployment or release ran. Git identity and core.worktree overrides
remain absent. Test children joined. Task scratch is removed after checks join,
and the shared Go cache is retained.

Friction: the missing foreign interfaces/implementations above, sandbox sockets,
Xwing context discovery's read-only session-index lock and Overwing's reported
ten-commit installed-source lag. The supplied worktree was reused.

STATUS: CONTINUE
