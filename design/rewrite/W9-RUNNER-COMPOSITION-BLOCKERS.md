# W9 runner composition blockers

Base HEAD: `1562be098`. Runner composition remains incomplete. This continuation
adds production composition probes and changes no production code. The foreign
contract gaps from the previous report remain unresolved on this branch.

`TestProductionRunnerNodeReachesNativeHost` uses the production private client,
authentication, state adapter, service bindings and native constructor. It reaches
`listen tcp 127.0.0.1:0`, where the sandbox returns EPERM. This is an explicit skip,
not a native execution pass. On a native host, the probe constructs the shared
hosts with `nodehost.NewNativeHost` and joins cleanup. The existing trigger-owner
probe shares this fixture and still fails before sockets because placement
eligibility is missing.

`TestProductionCloudExchangeReachesAwardRenewal` invokes the production
`run-node` bootstrap runtime with private award files for plan, eval and work.
Only external HTTP is replaced. Parsing, file acquisition, bootstrap exchange,
source/engine/resource comparison and production host selection remain real.
Work renews with the exchanged credential. Plan and eval each exchange once,
then fail before renewal with `controller awarded a unit this runner does not
execute`. The production factory supplies `runners.Hosting`, whose validator
accepts only trigger and node. Test cleanup restores the HTTP transport.
No factory, host or controller implementation is faked.

Contract changes and real integration still needed:

1. **W1/W2/W3: canonical owner placement.** Invariant: an owner executes a job
   only when the shared matcher accepts its live enrolled profile and sealed
   demand. The private host transport has no complete matching inputs or
   per-job eligibility projection. `NativeConfig.Local` remains nil. The lead
   must select that contract before W9 can bind it. Keep the committing
   self-claim's transactional matcher recheck. An unconditional predicate does
   not establish eligibility.
2. **DESIGN: W1/W2/W3 owner/eval result authority.** Invariant: evaluation
   reports use the live owner/eval lease and source-job fence without acquiring
   a work attempt. Annotation and summary still return HTTP 409 with
   `reason=holder_rights` in `TestReviewOwnerEvaluationReportsBeforeWorkAward`.
   `checkResultWriterTx` requires a live node holder and positive attempt.
   This retains the existing two-strikes design request. C1/C2 must define the
   committing source proof. Eval's fixed grants still omit NodeReport, which
   mask/cleanup need. Latest-reference binding still requires UnitNode. No
   broader grant or synthetic work authority was added.
3. **W8: production Cloud boundary.** `ProductionRuntime.Launcher` is unbound.
   `KubernetesBoundary.Probe` and `Clear` have no production implementations.
   W9 needs the real constructor or callbacks for enforced network evidence
   and reservation-scoped cleanup before connecting `launcher.NewLauncher`.
4. **W3/W8: Cloud plan/eval host dispatch.** The new bootstrap probe makes the
   post-exchange failure executable. W9 needs the declared unit-host boundary
   for plan and eval, including their private-state authentication and live
   lease renewal. The existing runner host cannot accept these units. Merely
   broadening its validator would dispatch both to the trigger owner path.

The swarm brief requires stopping a piece that changes another area's contract.
These requests stay in this area report. Lead-owned handoffs and amendments,
foreign contracts, grants and production defaults are unchanged. The earlier
source-checkout, here/cache/publication and scheduled-intake integration
requests remain in the prior W9 reports. Native self-hosted pipeline evidence
is still pending with the lead.

Verification used GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache and GOTMPDIR under /tmp/sw-astra-W9. Final probe
commands also set TMPDIR there. Package workers were limited to four.

Passed: whole-repository `go build ./...`; full short suites for cluster,
controller/client, controllerwire and clisurface; tagged cluster vet;
commentcheck against main; numeric read/control and debug-selector regressions;
all 24 metadata numeric-selector property seeds; and `git diff --check`.

The tagged cluster selection remains red for trigger placement, launch's
missing factory, and Cloud plan/eval dispatch. Start/warm reach signed-policy
HTTP, fetch-source reaches source-credential HTTP, and installed run-node
reaches bootstrap exchange. The separate metadata selection remains red for
owner annotation and summary. No native acceptance cell was newly unskipped,
no archived test was newly ported, and no full-repository test suite ran.

Shared file touched: `internal/cluster/production_runtime_integration_test.go`.
The other changed file is this report. Production additions/deletions are
**0/0**. No legacy code or caller was deleted or migrated. W9 production Go
versus main is **+16,945/-23,817, net -6,872**, within the -2,900 budget.
Tests, testdata, acceptance and contract declarations are excluded.

Mechanisms added: none. Friction: the contract gaps above, sandbox sockets and
Overwing's reported ten-commit installed-source lag. No Git write, installation,
operational Sparkwing command, pipeline, deployment or release ran. Git has no
local identity or core.worktree override. Test children joined. Worker scratch
is removed after checks complete. The shared Go cache is retained.

STATUS: CONTINUE
