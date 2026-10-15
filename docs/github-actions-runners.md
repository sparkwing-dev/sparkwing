# GitHub Actions runners

GitHub Actions execution uses the same runner claim, hosting and storage contracts
as other placements. The controller declares its OIDC exchange route. Native
exchange, runner startup and workflow generation still need integration evidence.
The sections below describe the target contract.

<!-- design:begin -->
## Repository consent and identity

A team binds a repository through its GitHub Actions registration. The workflow
selects that team. Repository and owner IDs come from verified GitHub evidence,
because display names and clone URLs can change or be supplied by a caller.

The workflow requests an ID token for the controller's audience with
`permissions: id-token: write`. `POST /api/v1/runners/github/exchange` verifies the
OIDC evidence and current registration before issuing a root capability. The
controller derives its team, repository, ref, SHA and workflow-run scope.
User-supplied labels grant no repository or pool authority.

OIDC evidence establishes the workflow's push scope. Verified default-branch
trust comes from a signed App delivery. See [GitHub App](github-app.md) and
[credentials](credentials.md).

## Claims and execution

`POST /api/v1/runners/claim` scans one ordered queue for the requested trigger
and job kinds. A mixed request needs both claim grants. Named owner and job
claims use the corresponding run-scoped claim routes. Every selection checks
repository authority and push scope.

Each award carries a fresh capability, a binding, granted runner identity and
immutable execution inputs. The reusable Actions credential stays in the runner.
SDK processes receive delegated process authority through protocol hello.
Claims and exchanges never retry automatically after a lost one-time response.
Lease expiry resolves an uncertain award.

The acquired checkout uses its sealed location and exact SHA. Its SDK pin selects
the verified matching engine before installation, planning and execution. A
workflow checkout alone cannot replace those accepted inputs.

## Data and source

Data reads and writes use signed `/data` transfers and authenticated record
operations. A live claim needs its own authorized read edges and write scope.
Repository membership alone does not make every object readable. Pipeline
binaries remain local to the executing machine.

Source acquisition uses the live run claim at
`POST /api/v1/runs/{run}/source-credential`. The controller checks sealed
repository authority and records the release before returning a credential.
See [team git credentials](git-credentials.md).

Revoking a registration or credential stops later controller operations. It
cannot recall GitHub tokens, provider sessions or signed URLs already issued.
<!-- design:end -->
