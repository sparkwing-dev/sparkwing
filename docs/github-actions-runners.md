# GitHub Actions runners

GitHub Actions execution uses the same runner claim, hosting and storage contracts
as other placements. The controller verifies OIDC exchange
through the shared capability policy. Native runner startup and workflow execution
require separate integration evidence.

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

## Credential issuance

The exchange issues a controller capability with `nodes.claim` and
`triggers.claim`, a one-hour lifetime, and the verified repository, ref, commit
and workflow run binding. Repository identity uses GitHub's numeric ID, so a
rename preserves that identity. An owner transfer requires a matching new
binding.

The request takes `id_token` and `team`. The controller derives grants, push
facts, expiry and runner identity. Its response carries `token`, `team`,
`repository`, `expires_at` and `labels`. The token is returned once. The exchange
has no automatic retry because a repeated request cannot recover a lost bearer.

`POST /api/v1/runners/claim` accepts the requested trigger and job kinds. Each
kind requires its own claim grant, and the store checks repository and push
authority before awarding work. Source, secrets, data and execution reporting
use the separate capability awarded for that unit. A runner root cannot read
them or a pipeline's sizing records by itself. Discovery routes remain
available to live roots.

The cache-grant protocol and its issuance route are removed. Byte-storage
access uses the authenticated data adapter and the unit's live authority.


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
