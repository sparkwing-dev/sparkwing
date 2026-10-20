# W9 local intake continuation

The real local controller now admits runs through the same intake policy as the
cloud controller. `controller.New` installs `Server.ResolveIntake` before serving.
The invariant is that every controller resolves captured preferences and trusted
deployment ceilings in the admission transaction before sealing a request.

This worktree started at 0b26498ec without W6's intake implementation. The required
policy, transactional settings lookup, shared placement function, URL validation
extraction and regression tests were brought over from W6 at 3318907c7. The
ResolveIntake entrypoint uses this checkout's existing settings representation.
When combining with W6, retain W6's native settings implementation and its
intakeOrigins helper. There is one policy and one constructor, with no local copy.

`TestProductionLocalControllerAdmitsRun` first failed with HTTP 503 and
`intake policy is not configured`. It now opens the real local controller with
an isolated store/key, reconciles it, and calls its ServeAPI over net.Pipe with
transport peer identity. No store, intake policy, claim resolver or controller
factory is substituted. Signed release metadata replaces external fetching.
The test covers explicit zero priority, local source preservation, no local owner
deadline, immutable replay, owner claim, plan acceptance, run/node transitions,
output upload/readback, durable log upload/readback, successful completion, public
status and the end of owner authority. It reports execution through the real API.
Native pipeline spawning and machine admission remain with the lead's native run.

The rest of the local run's composition is present:

| Seam | Construction or default |
| --- | --- |
| ProvisionPolicy | store.Open installs nativeProvisionPolicy. Local queue credentials use peer delegation. |
| CapabilityManager | Server selects Tenant or Operator when no replacement resolver is supplied. |
| Owner/node claims | Local binder installs Claimant as the profile resolver and signed runner admission. |
| Plan/settings | Store selects shared intake/plan contracts. WithSecretsCipher installs secret preflight and session ciphers. |
| Renewal/state | Owner and node private transports bind authenticated state, binding checks and independent lease renewal. |
| Execution/evaluation | NativeServices binds source, snapshots, processes, machine admission and per-node grants. Nil Evaluate selects claims.evaluate. |
| Process services | WithClient supplies secret, output, latest, OIDC, annotations, summary and child operations. |
| Bytes/records/logs | WithDataObjects configures data authority/access and result manifests. WithClient supplies storage and log uploaders. |
| Completion | FinishRunAndUnit commits run and owner completion. Signed version policy supports public status. |

No other missing policy or adapter was found on this path. An additional inventory
probe returned 403 for a local OS peer because the token-list route requires a
member credential. This is separate from CapabilityManager composition and from
local execution. W2/lead should decide any change to that authority contract.

Verification passed with GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOTMPDIR=/tmp/sw-astra-W9/go and TMPDIR=/tmp/sw-astra-W9:

- Build and vet in both root and .sparkwing modules, with package concurrency 4.
- The new real-controller lifecycle test, including under race.
- Focused CLI local run, controller readiness and local service tests.
- Focused store admission, plan, expansion, provisioning and authority tests.
- Focused controller intake, source, receipt, retry, private host and storage tests.
- Full short localcontroller and doccheck suites, and the full settings suite.
- Intake provenance fuzzing: 172,520 cases in ten seconds, no failures.
- Commentcheck against main, clean across 2,539 files, and git diff --check.

No acceptance cell is newly claimed and no archived test was newly ported. W6's
intake policy and provenance tests were reused. Source documentation is updated
in internal/wingd/LOCAL-CONTROLLER.md. Shared files touched: that document,
internal/settings/intake.go and intake_policy_test.go, internal/controller/server.go,
intake_source.go, intake_policy.go and intake_policy_test.go, and
internal/store/intake_settings.go, plan_contracts.go, plan_seal.go and expansion_accept.go.
The W9 test is cmd/sparkwing/local_controller_policy_test.go.

No whole legacy file was deleted. Production Go is +210/-23, net +187, largely
W6 prerequisites missing from this checkout. W9-owned production remains
+16,916/-23,812, net -6,896 versus main, within its -2,900 budget.
No new cross-area contract change is required for the run path. Friction was the
missing W6 prerequisite and sandbox restrictions on native sockets. No Git write,
candidate install, operational Sparkwing command, service or release occurred.
Git has no local user identity or core.worktree override. Scratch was removed
after checks joined. The shared build cache remains.

STATUS: COMPLETE
