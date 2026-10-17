# W2 run-resource existence privacy

<!-- design:begin -->

Implemented lead decision 1 from 2026-10-17 04:00. Invariant: an authenticated
caller cannot distinguish a foreign run from a missing run through status,
error body or decoder diagnostics.

Every run route now uses the existing post-authentication tenant selector
boundary. Decoders retain only path selectors and body evidence. Removed
EngineController.runTarget and its global Operator.RunTeam lookup. The router
resolves the target before returning decoder errors, including malformed named
claims, child intake and failure-generation selectors. Expansion source-node
reads moved to post-authentication resolution in the same tenant. Result-body
digests, child lineage selectors and stored source-generation fencing survive.

Production changes are +69/-105 lines, net -36. W2 production against main is
+17,124/-18,016, net -892, within the -250 budget. The count excludes client,
tests, testdata, acceptance harnesses and contract declaration paths, matching
the lead's area filter. No compatibility API or new mechanism was added.

## Tests first and verification

Both required tests failed before the change and pass unchanged afterward:

- TestTeamBoundaryOutputHidesRunExistenceProperty
- TestTeamBoundary_EveryRunRouteHidesOtherTeamsResources

The first reproduced foreign 403/team versus absent 404. The second reproduced
the same class across private, child, result and reporting operations. No test
file or assertion changed, and no historical test was ported in this session.

Passed with GOWORK=off, GOFLAGS=-buildvcs=false, GOCACHE=/tmp/sw-gocache,
GOTMPDIR/TMPDIR under /tmp/sw-astra-W2 and at most four package workers:

- Whole-repository go build -p 4 ./....
- Focused go vet for controller, capability and apierr.
- Full capability and apierr tests.
- Credential, authority, deployment, engine-authority and local-node acceptance
  selection, 3.610s. No native topology cell was unskipped.
- Controller recorder selection, 40.860s. It includes both unchanged privacy
  tests, policy/credential/serving properties, engine/private routes, terminal
  result replay, owner handoff, expansion source fencing and child binding.
- Both privacy tests, terminal result replay, owner handoff, expansion fencing
  and child binding under -race, 15.177s.
- Commentcheck against main across 2,686 Go files, gofmt and git diff --check.

The broad controller attempt failed only at the socket restriction in
TestMetricsAddr_MovesMetricsOffTheAPIListener,
TestMetricsAddr_FailsStartupWhenTheMetricsPortIsTaken and
TestSourceCredential_AppTokenReadsTheVerifiedRepository. The recorder selection
omits socket families and skips
TestTeamBoundary_ResolveWaiterDoesNotReadAnotherTeamsLeader, whose real client
also requires a listener. PostgreSQL remains unexercised without its test URL.
No full controller or repository test pass is claimed.

## Contracts and remaining integration

C2 now records the approved target-resolution boundary and evidence preservation.
No Go signature, schema or other area's contract changed. Lead-owned amendments
and handoffs remain untouched. Earlier W2 serving, bootstrap and operator-team
work remains committed. C1's QueuePriorityWriter transaction remains the prior
separate integration point, with its existing fake-backed HTTP contract.

The supplied native receipt
/home/bombuntu/code/ink-labs/wing/architecture-2026-10-14/review/areas/W2-existence-native-latest.md
was absent at session start. Completion requires the lead harness to commit the
patch, run its native tests and produce that file with no FAIL. There is no new
design question or contract request.

All changed production files belong to W2: authority_routes.go,
child_routes.go, engine_claim_routes.go, engine_job_control.go, engine_routes.go,
log_submission_routes.go, metrics.go, missing_operation_routes.go,
policy_router.go, private_host.go, run_selector.go and source_controller.go,
all under pkg/controller. No caller outside W2 required migration. The only
contract edit is design/rewrite/contracts/c2-credentials.md.

Friction: the native receipt is missing and this sandbox refuses sockets. No
Git write, candidate binary, service or pipeline ran. Scratch is removed before
handoff and the shared build cache is retained.

<!-- design:end -->

STATUS: CONTINUE
