# W8 launcher review reconciliation - 2026-10-17

Continues 788701c0e and the integration merge of the integration branch. The lead's
native report after session 2 has nine passing packages and no FAIL. It covers
the previous committed state. This worker leaves that external report unchanged.
The lead must commit this work and rerun native verification before accepting it.

## Merge

Resolved the contents of all five conflicted files: .apidiff/pkg_store.txt,
contracts/c1-data.md, contracts/c3-controller-wire.md,
contracts/c4-surface-config.md and internal/store/node_state.go. Both launcher-rejection
and unpriced-claim transition edges survive. Both sides' independent contract
amendments survive. The store API snapshot is regenerated from the resulting
source, so integration-deleted APIs remain deleted. No integration-deleted file
or implementation was restored. No lead-owned amendment or handoff file changed.

The swarm brief forbids Git writes. The lead must stage these resolved files
and finish the merge. Their index entries still read UU. No conflict marker
remains in their working contents. No commit, reset, rebase, stash or Git metadata
write ran.

## Findings

- B1. A node's team must match its provisioner, and a used node must never move
  to another team. Both required regressions failed on the previous code.
  TeamNodeLabel now uses node-restriction.kubernetes.io/sparkwing-team-node.
  Reservation checks the NodeClaim UID, status.nodeName and providerID, then its
  owning NodePool UID and template label. NodeClaim and Node labels must agree.
  The protected sparkwing-used-team label retains first use through Clear.
  Even retargeting the Node, NodeClaim and NodePool together cannot move a used
  node. Provisioner and cross-team fuzz properties pass.
- S1. No customer Pod may receive AWS credential injections. Removed the IRSA
  and Pod Identity environment, mount and token-volume allowances. Region
  settings remain allowed. Negative tests and a credential-injection fuzz
  property cover containers and init containers. The chart's customer account
  has no role annotation. Docs prohibit Pod Identity associations. Canaries use
  that same unprivileged account and need no AWS credential allowance.
- S2. Only a typed Pod/spec mismatch may reject an award. Pod-list or
  PriorityClass read errors remain transport errors. Activation and later
  reconciliation tests retain the Job and recover on the next successful read.
- S3. Active unsuspended Jobs require exactly one owned Pod. Activation polls
  every 50 ms for up to two seconds. Missing or duplicate Pods reject. The
  regression starts with an empty list and supplies the Pod on a later read.
  Tests no longer create Pods during the activation patch. The fake Job
  controller supplies Pods on reads, with distinct Job UIDs.
- S4. A rejected node cannot return to capacity. CloudBoundary.Retire cordons
  the exact reservation's node and deletes its NodeClaim by UID. Failure retains
  cleanup ownership instead of releasing. Repeated retirement is idempotent.
  A real SQLite/controller rejection test checks failed award, cordon and absent
  NodeClaim. A fuzz property rejects changed reservation and node identities.
- S5. Malformed binding, reservation, requested-template, deadline and bootstrap
  annotations trigger UID-fenced Job deletion. Garbage collection owns Secrets
  when their cleanup references are unreadable. The node reservation remains
  subject to current claim authority and vacancy. Successful deletion permits
  further claims. Tests cover every annotation class.
- NITs. Removed LimitRange list permission. Validate ENI JSON fields, ENI/MAC
  identities, VLAN range and IPv4/IPv6 subnet membership. Document the complete
  explicit default list. PriorityClass defaults come from the cluster and are
  tested with explicit API-defaulted Pod fixtures, including priority tampering.
  operator teams show reports the protected label key/value in all output
  modes. LauncherFactory now carries a file-backed bearer reader. Every
  controller request rereads the token, so rotation needs no restart.

## Contracts and callers

Integration authority was used for these contract changes:

- C4: protected placement/first-use labels, Karpenter proof, CloudBoundary.Retire,
  NewProductionBoundary's dynamic client, polling, rejection and credential-file
  reader. These supersede the earlier Pod Identity/empty-list allowance.
- C2/C3: OperatorManager.ReadOperatorTeam and OperatorTeam. The existing team
  GET accepts current deployment operator.tokens with BindOperatorSettings and
  returns only team/display_name. Console sessions retain their existing view.
  A generated authorization property covers missing grants, expired/forged
  presentations and adversarial query/header selectors. It checks the exact
  two-field deployment projection. The CLI uses the shared TeamNodeValue owner.

Shared production files: internal/capability/routes.go,
internal/clisurface/registry.go, internal/controllerwire/operator.go,
internal/controller/operator.go, internal/controller/client/operator.go,
cmd/sparkwing/operator_team.go and cmd/sparkwing/help_registry.go.
The only shared transition edit is the node_state.go merge resolution.

Shared caller migrations: internal/controller/burst_cloud_job_test.go and six
launcher_*_w8_test.go acceptance fixtures. The burst fake gains Retire and
supplies Pods on read. The acceptance boundary fixtures now provide NodeClaims
and NodePools. A new operator_team_read_test.go tests the deployment projection.
Generated changes: .apidiff/pkg_store.txt, .apidiff/pkg_controller_client.txt,
api/openapi.yaml, API/CLI reference docs, the vendored runner chart and embedded
doc/changelog mirrors. Source docs include docs/auth.md and chart/launcher docs.
No contract change is left waiting on another area. Existing W8 amendment and
handoff implementations are retained. Native deployment proofs remain open.

## Verification

All Go commands use GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOMAXPROCS=4, -p 4 where supported, and GOTMPDIR/TMPDIR under /tmp/sw-astra-W8.

- Whole-repository build passes. Vet passes for all changed packages and direct
  dependents, including controller, client, command and acceptance test fixtures.
- Full launcher, charts and capability suites pass. Cluster passes with its
  socket-dependent metrics-listener test excluded.
- Focused acceptance passes: TestLauncher*, TestStoreLauncher* and
  FuzzStoreLauncherReservationFenceW8 seeds. This includes all three unit kinds,
  persisted reservation cleanup, source/credential publication and committed
  failed-award rejection. Native catalog cells and historical test ports are
  unchanged. No new native acceptance claim is made.
- Command/operator/client/registry tests pass. Real-store command tests check
  plain, pretty and JSON output plus the identity-only HTTP projection. Both
  saturated Cloud worker and Cloud retry worker burst tests pass.
- Race selection passes for competing reservations, preparation lease ownership,
  activation cardinality/API errors and PriorityClass defaults.
- Generated fuzz runs pass: cross-team reservation 1,049 executions; provisioner
  matching 1,028; retirement identity 1,021; AWS credential injection 1,000.
- API-reference, OpenAPI, command-reference, clisurface, embedded-doc scrub and
  changelog drift checks pass. Commentcheck and diff whitespace checks pass.

Two retained socket suites cannot run here: the controller operator-console
fixture and pkg/docs TestWebClient_Versions_HappyPath panic when httptest tries
binding a port. The lead must run those natively. No test expectation was weakened
for the sandbox. Focused non-socket checks are green. The API-reference/OpenAPI and clisurface full suites pass. Embedded-doc and
changelog checks pass after the final mirror synchronization. The lead owns
independent review and the native rerun for this uncommitted state.

## Deletion, budget and remaining work

W8-owned production Go this session: +337/-76, net +261. Deleted the obsolete
credential-injection helpers and captured-token contract. Removed the custom
team label spelling and unused LimitRange rule. No whole production file was
deleted this session. Earlier launcher admission-policy and legacy runtime cuts
remain deleted. No compatibility wrapper was added.

W8 production Go against current main: +12,732/-6,902, net +5,830, excluding
tests, testdata and contract declarations and including the two new source files.
Directory net: cluster -1,536, runners +2,457, engine +2,541, wingd +2,368,
charts 0. The -2,300 budget remains unmet. The prior work includes signed
engine resolution/installation, cross-platform local-controller lifecycles,
network probes, exclusive reservations and persisted teardown. Those required
contracts were not sized by the deletion-only budget. This session's growth
implements the lead's NodeClaim/NodePool proof, retirement, typed verification,
explicit activation check and ENI validation. Removing them would reopen the
assigned findings. This is a justification for lead review, not a budget waiver.

Remaining real integration: provisioned EKS nodes and images, real NodeRestriction
refusal under a kubelet identity, effective RBAC across all bindings, native
API-defaulted Job/Pod verification, escaped-node networking, DinD termination,
scratch reclamation and live projected-secret rotation. The approved post-create
verification interval remains. Windows/macOS and whole-placement acceptance stay
with the lead's native lanes. No deployment, installation, service, release or
pipeline ran. No foreign implementation stub blocks these launcher/chart fixes.

Friction: Git index completion belongs to the lead under the no-Git-writes rule.
Socket tests require the native harness. Overwing discovery reports an older
installed binary but works. Git config has no local identity/core.worktree
override. Worker scratch is deleted after verification. Shared cache is retained.

STATUS: CONTINUE
