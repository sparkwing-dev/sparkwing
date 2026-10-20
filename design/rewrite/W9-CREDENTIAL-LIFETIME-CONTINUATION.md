# W9 runner composition and credential lifetime

Base `76e879771`, environment `1087`. Runner composition remains incomplete.
The existing production-command probes reproduce the prior report's four gaps.

Invariant: closing a private host prevents every retained service client from
presenting that host's award credential in a later operation.
`NewLocalHostClient` now binds the award provider to the host's lifetime.
It checks closure before and after obtaining the credential. Process, storage
and log clients share this transport. Independent node transports retain their
existing closure checks. No grant, issuance policy, persistent binding or
foreign contract changes.

`FuzzLocalHostCloseRetiresProcessClients` creates the process client through
the real controller and SQLite private transport. It varies annotations,
summaries, secrets, OIDC, output, latest references, child intake and child reads,
with generated service values. All eight seeds first failed because the retained
client still contacted the controller after host closure. They now pass, including
under race. Two seconds of fuzzing completed 18 executions without a failure.
The C3 wire documentation records this lifetime behavior.

The integration points still open are:

- **W1/W2/W3 owner placement.** Trigger `start`, `warm` and native binding fail
  with `owner hosting requires canonical placement eligibility`. C1 owns
  `readMatchingInputsTx`, including enrollment, ancestor repository scope,
  sealed requirements, resources, selected runner and allowed pools. C3 has no
  projection of that decision. The smaller contract would return the canonical
  per-node eligibility with the authenticated owner snapshot, bound to the
  node generation and declaration hash. W9 can then supply `NativeConfig.Local`
  without a second matching policy. The transactional self-claim must still
  recheck eligibility. This needs a C1 contract change before implementation.
- **DESIGN: W1/W2/W3 evaluation reporting.** Owner annotations and summaries
  still return HTTP 409 with `holder_rights` before any work award.
  `checkResultWriterTx` requires a live node holder and a positive work attempt.
  Owner-source reports need an explicit source generation/declaration proof
  checked under the owner or eval lease in the committing transaction. Eval's
  fixed C2 grants also omit NodeReport, which masks and cleanup require.
  Owner latest references still require a node-claim parent relation. These
  need one approved source/reporting contract, rather than another exception
  to the attempt writer. The previous two-strikes DESIGN request stays open.
- **W8 launch boundary.** `ProductionRuntime.Launcher` remains nil. The only
  concrete `KubernetesBoundary` requires Probe and Clear callbacks. No production
  callbacks exist in this checkout. W9 needs a constructor implementing the
  existing CloudBoundary contract, with measured network enforcement and
  reservation-scoped cleanup. A fake or nil callback in the executable would
  not establish that boundary.
- **W3/W8 plan/eval hosting.** Both bootstrap awards exchange once but fail with
  `controller awarded a unit this runner does not execute` before renewal.
  `runners.Hosting`, `nativeInputs` and `NewRunInput` implement trigger/node
  hosting. The latter constructs a trigger owner. These units need real hosts
  behind the existing UnitHost contract, with their own private-state access
  and lease renewal. Broadening the validator alone would misdispatch them.

The existing BootstrapRuntime tests pass against the declared host/source
interfaces with fakes. The production probes stay red. Node `start`, `warm` and
native binding reach the sandbox's EPERM socket boundary. Installed start/warm,
fetch-source and run-node reach their production HTTP operations. Cloud node
exchange reaches renewal. No native pipeline execution is claimed.

Numeric-selector production work was already complete. Public read/receipt/cancel,
debug/resume, metadata authority and debug team-isolation regressions pass.
No native acceptance cell was newly unskipped and no archived test was ported.

Verification passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/gotmp,
TMPDIR=/tmp/sw-astra-W9 and at most four package workers:

- Whole-repository build.
- Vet of controller/client, controller, cluster and localcontroller.
- Full short suites for controller/client, cluster, localcontroller and runner.
- Focused private-host, annotation, numeric-selector and authority properties.
- New lifetime property under race and fuzzing.
- Commentcheck against main across 2,573 files, sleepcheck against HEAD and
  whitespace checks.

Production Go changes are **+17/-1, net +16**. The removed line constructed the
unfenced retained transport. No legacy file or external caller was removed.
W9-owned production Go versus main is **+16,961/-23,817, net -6,856**, within
the -2,900 budget. Mechanisms added: the existing bearer provider now checks the
existing host lifetime. It adds no public API, setting, route or storage.

The shared file touched is `internal/controller/private_host_test.go`. The other
changes are W9's `internal/controller/client/private_host.go`, C3 wire documentation
and this report. Lead-owned handoffs/amendments and foreign contracts remain
unchanged. Independent review and native pipeline execution remain with the lead.

No Git write, candidate install, operational Sparkwing command, pipeline,
deployment or release ran. Git identity and core.worktree overrides are absent.
All checks joined. Task scratch is removed at the end. The shared cache is kept.
Friction is the missing production contracts/implementations above, sandbox
sockets and Overwing's reported installed-source lag.

STATUS: CONTINUE
