# W9 redesign full-suite corrections

The reported regressions were reproduced before editing. Their fixtures still
supplied inputs from before the entrypoint, environment and registration cuts.
Production Go behavior is unchanged.

- Burst acceptance now changes the SDK entrypoint with the selected proof
  pipeline and includes that entrypoint in its accepted resolution. The fixture
  previously retained CI from the source-upload fixture while executing the
  burst SDK. Here, personal and Cloud awards use the same accepted identity.
- The local two-job controller fixture records EnvironmentCaptured beside its
  captured snapshot. Production CLI and schedule intake already record it.
- The repository example test selects SDK metadata by Example. It still checks
  that arguments are a present empty array and plans the invoked example alias.
- The private-transport revocation fixture issues a real team-admin credential
  before revoking the shared machine. The 09:55 authorization rule is unchanged.
- Regenerated api/openapi.yaml from internal/apispec. Its Source schema now
  includes requested_ref.

Invariants retained: execution uses the accepted SDK entrypoint, captured
snapshots name their provenance, and shared-machine management requires current
team administration. Existing environment-fence and machine-administration
property tests cover the authorization boundaries. No new security mechanism
or exception was added.

Validation uses GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR=/tmp/sw-astra-W9 and at most go test -p 4.

- Repository build and focused vet pass.
- Full localcontroller, nodehost and apispec suites pass.
- Reported failures pass in the focused selection, including uploaded-source
  execution, all three native SDK award placements and requester-secret masking
  seeds. The final controller selection passes in 12.942s and also covers the
  Build-to-Deploy child and submitted-environment generator evaluation.
- CLI source-selection and trigger-inspection regressions pass. Machine-setting,
  shared-machine administration and live environment-fence properties pass.
- TestSurface* and TestEngineAmendments* pass. No cell was newly unskipped and
  no archived test was newly ported.
- rewrite_integration tests compile for cmd/sparkwing and pkg/controller.
- Commentcheck against main is clean across 2,860 files. Whitespace checks pass.

The broader controller suite cannot complete here. TestReadHostKeyTakesTheKeyTheServerPresents,
TestNativeLocalControllerScheduleFiresOneSealedRunAtDueInstant and
TestDashboardStreamsOutliveTheListenersWriteTimeout reach the sandbox's socket
refusal. The lead must run the full repository and socket-dependent native suites
after committing. The supplied W9-burst-native-latest.md is green for the prior
committed state, dated 2026-10-17 10:37 MDT. It does not verify these edits.
Deployed Cloud isolation and native platform/release proofs remain lead-owned
integration evidence under A1-53.

Deleted/replaced 11 stale test lines. Production added/deleted this session:
0/0. W9-owned production Go versus main is +19,771/-25,208, net -5,437,
within the -2,900 budget. The previous source/child/environment redesign is retained.

Shared files touched: pkg/controller/burst_acceptance_test.go,
pkg/controller/engine_claim_sdk_unix_test.go,
internal/wingd/localcontroller/node_pipeline_test.go,
internal/nodehost/owner_process_store_test.go and api/openapi.yaml.
No contracts changed or remain requested by this correction. Lead-owned
handoffs and amendment files were read and left unchanged. No Git writes.
Scratch is deleted at the end. The shared Go build cache is retained.

STATUS: CONTINUE
