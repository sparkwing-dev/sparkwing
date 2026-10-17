# W8 EKS Cloud example continuation - 2026-10-17

Continues committed launcher/chart work at 5026096d3. The native report read at
startup has passing Go packages and one failure: Helm cannot open the requested
repository-root examples/cloud.yaml. The admission-policy deletion and Pod/award
rejection path were already committed and were retained.

## Changes and regression evidence

- Added examples/cloud.yaml as a relative symlink to the chart's packaged example.
  Both invocation paths use one values file. The Cloud rendering and credential
  refusal tests exercise the repository-root path used by the native harness.
- Enabled the existing volumePermissions initializer in that example. fsGroup
  grants group access but cannot let uid 65534 chmod a root-owned /data to 0700.
  The rendered-example regression asserts the root chown command, target identity
  and writable data mount. It first failed on the missing root path, then on the
  absent initializer, and passes with both fixes. Helm template and lint pass.
- Extended activation tests for each allowed EKS injection, their combination and
  IPv6 Pod Identity. Each passes immediate readback and later reconciliation,
  with its Job retained. A shared test helper supplies Pods on activation.
- Corrected ownership guidance in the chart README, C4 contract and changelog.
  Regenerated the embedded changelog through sync-docs.

Invariant retained: a created Pod may differ from its requested spec only by
Kubernetes defaults and the named EKS injections. Every other mutation deletes
its Job and fails the exact live award. Existing negative activation tests,
reconciliation tests and the real SQLite/controller acceptance test pass for
added containers, changed images, removed securityContext fields and extra
hostPath volumes. The generated mutation property also passes.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false,
GOCACHE=/tmp/sw-gocache, GOMAXPROCS=4 and at most four package workers.
GOTMPDIR and TMPDIR are under /tmp/sw-astra-W8.

- Whole-repository go build -p 4 ./... passed.
- Full charts and launcher suites passed: 53.537s and 3.259s.
- Focused acceptance selection passed (1.258s):
  TestLauncherPodRejectionCommitsFailedAwardW8, TestStoreLauncherW8,
  TestStoreLauncherReservationW8 and TestLauncherControllerW8.
- Vet for charts and launcher passed. Commentcheck against main passed across
  2,787 files. Diff whitespace check passed.
- Helm template from the repository root with -f examples/cloud.yaml and Helm
  lint with those values passed.
- Embedded-doc and changelog drift tests and sync-docs --check passed.
- FuzzPodRejectsUnexpectedMutation: passed 1,050 executions.

No historical test was newly ported and no native acceptance cell was activated.
The required native report still contains its original Helm FAIL. It describes
the last committed state and must be replaced by the lead harness after commit.
This worker does not edit that report or claim a native pass for this change.

## Deletion and budget

Production Go added/deleted this session: 0/0, net 0. No production file was
removed this session. The earlier admission-policy deletion remains removed;
owned production code and rendered charts have no admission-policy API dependency.
No production caller migration was required.

W8 production Go against main: +12,457/-7,142, net +5,315, excluding tests,
testdata and contract declarations. Directory net deltas: cluster -1,551;
runners +2,211; engine +2,541; wingd +2,368; charts 0. The -2,300 budget remains
unmet. The inherited overage includes signed selected-engine/index/install
support, cross-platform local controller lifecycles and the launcher's measured
network boundary, exclusive-node reservations and persisted teardown. Those
serve required contracts. This small continuation adds no implementation or
mechanism to that overage and claims no budget waiver.

## Contracts, shared files and remaining integration

Contract changes made: C4's Cloud example paragraph now specifies the existing
ownership initializer along with fsGroup. No Go contract shape changed.
Contract changes needed in other areas: none for this continuation.
Lead-owned handoffs and amendments were read and left unchanged. Previously
finished amendment and handoff implementations were retained.

Shared files: examples/cloud.yaml, CHANGELOG.md, pkg/docs/changelog.md and
contracts/c4-surface-config.md. Other changes stay in charts and the launcher's
test file. No shared production Go file or external API caller changed.

Real EKS integration still needs actual images and credentials, provisioned
Ready team nodes, effective RBAC across all bindings, baseline NetworkPolicies
that exclude sparkwing-job, escaped-node enforcement and native DinD/scratch
teardown evidence. The existing production boundary owns those checks. No
external deployment or service state was changed. Native Windows/macOS and full
placement QA remain with the lead's platform lanes. No fake test result here
claims those proofs. The post-create timing residual remains the approved one:
a Pod absent immediately after activation is verified on later reconciliation.

Assumption: the repository-root example path should work while the packaged
chart retains its example. A relative symlink avoids two diverging values files.
Undo: remove that link and use the packaged chart path in the harness.
No setting, flag, environment variable, retry, interface or production mechanism
was added. The ownership fix uses the chart's existing initializer.

Friction: the native failure came from a missing path, while the deployment log
also exposed the independent ownership defect. Overwing discovery works but
reports an older installed binary. Native sockets/deployment checks belong to
the lead harness. No Git write, pipeline, installation or external report write
ran. Git config has no local identity/core.worktree override. Worker scratch is
removed after verification. The shared build cache is retained.

STATUS: CONTINUE
