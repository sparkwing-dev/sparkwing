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
workflow run, run attempt and workflow reference binding. Each repository/run/attempt
can exchange once, including after the first credential is revoked. A team may
bind any number of repositories and hold any number of live Actions credentials;
a credential outlives its job, so counting them would cap workflow throughput.
Repository identity uses GitHub's numeric ID, so a
rename preserves that identity. An owner transfer requires a matching new
binding.

The request takes `id_token` and `team`. The controller derives grants, push
facts, expiry and runner identity. Its response carries `token`, `team`,
`repository`, `expires_at`, `labels` and the verified `binding`. The token is returned once. The exchange
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
repository authority. Actions source acquisition uses the workflow's own
`GITHUB_TOKEN`, scoped to the registered repository. It receives no team
credential or App token.
See [team git credentials](git-credentials.md).

Revoking a registration or credential stops later controller operations. It
cannot recall GitHub tokens, provider sessions or signed URLs already issued.
Set `GITHUB_TOKEN` to `${{ github.token }}` on the runner step. The generated
workflow includes this setting. The token stays in source acquisition and does
not enter the pipeline environment.

## Workflow

Commit this workflow as `.github/workflows/sparkwing.yml`, replacing the team
and controller URL with your registration values. The runner uses the
`github-actions` label.

```yaml
# Runs Sparkwing work for this repository on this repository's GitHub
# Actions minutes. The controller hands the job only triggers and nodes for this
# repository, and the job exits once the queue has been empty for --idle-exit.
name: sparkwing
on:
  push:
  workflow_dispatch:
permissions:
  id-token: write
  contents: read
jobs:
  sparkwing:
    runs-on: ubuntu-latest
    # The runner credential lives one hour, so the job does too.
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v4
      - name: Install sparkwing-runner
        run: |
          base=https://github.com/sparkwing-dev/sparkwing/releases/latest/download
          curl -fsSLO "$base/sparkwing-runner-linux-amd64"
          curl -fsSLO "$base/SHA256SUMS"
          grep ' sparkwing-runner-linux-amd64$' SHA256SUMS | sha256sum -c -
          install -m 0755 sparkwing-runner-linux-amd64 "$RUNNER_TEMP/sparkwing-runner"
      - name: Run this repository's Sparkwing work
        env:
          GITHUB_TOKEN: ${{ github.token }}
        run: |
          "$RUNNER_TEMP/sparkwing-runner" warm --github-actions \
            --team acme \
            --controller https://sparkwing.example.com \
            --idle-exit 2m
```
<!-- design:end -->

## App-triggered runs

On Sparkwing Cloud an App push or pull request run goes to the Cloud launcher
unless its automation or subscription selects `runner: github-actions` or
`runner: pool=<name>`, or a repository pool route rule requires a pool. Each of
those seals host dispatch. With `github-actions`, only an Actions runner whose
push binding matches the delivery's repository, ref and commit may claim the
run's trigger; other team machines are refused. A create-run request cannot
select `github-actions` itself. See [GitHub App run owner](github-app.md#run-owner).

The runner requests a GitHub ID token with the controller URL as its audience,
then exchanges it for a short-lived controller capability. The exchange returns
the verified push binding; the runner uses that repository and expiry for its
claims. The workflow needs `id-token: write` and `GITHUB_TOKEN` for source access.
The exchange works once per run attempt, so the runner keeps the version
`.sparkwing/go.mod` pins rather than updating itself; a pin that signed policy
revokes or puts below the minimum fails the job and names the pin to raise.

The Actions job fails when Sparkwing work it executed fails. `warm` exits
non-zero and names each failed or cancelled run and job with the
`sparkwing runs status --run <id>` command that shows why. An idle exit with no
claimed work succeeds.

`warm` uses controller-awarded leases and job settings. It rejects the removed
`--heartbeat`, `--lease`, `--holder-prefix`, `--trigger-source`,
`--accept-working-tree`, `--cpu-ceiling`, `--memory-ceiling`, `--deadline` and
`--warm-module` flags. Configure resource ceilings on the Kubernetes launcher;
configure job timeouts in the pipeline. Pipeline lint always analyzes source
without running pipelines; its removed `--static` flag is rejected.

`--max-concurrent` defaults to one job. Trigger owners have separate capacity
with the same bound, because an owner waits for its jobs to finish. The default
runner can hold one owner and execute one job at a time.

## On-demand fallback

A repository `placement.prefer` list can end with a `github-actions` tier.
The controller dispatches one workflow per job queue generation once earlier
holds permit it. Team runners remain eligible while dispatch is outstanding.
GitHub bills the repository account for fallback startup and execution, including
workflows that lose the claim race.

A team owner first enrolls the repository as an Actions runner repository,
connects its GitHub App installation and enables repository automation or a
pipeline subscription. Approve the workflow ref and hosted image with an
authenticated `PUT /api/v1/team/github-app/actions-approval` request:

```json
{
  "repository_id": 123456,
  "workflow": ".github/workflows/sparkwing-fallback.yaml",
  "ref": "refs/heads/sparkwing-fallback",
  "runner": "macos-latest"
}
```

Repeat for each approved image. The controller dispatches an image only when the
job's `requires` admit a GitHub-hosted runner on it: the image fixes the operating
system, and such a runner carries no other labels. A tier whose image the job
excludes shows as unavailable instead of spending minutes on an idle workflow.
`DELETE` on the same endpoint withdraws an
approval using the repository ID, workflow and runner fields. Replacing or
withdrawing approval invalidates its earlier receipts. The repository's default
branch must contain the workflow. The approved branch supplies the workflow
revision, while the original event supplies the workload revision.

The approved ref must be a dedicated branch such as
`refs/heads/sparkwing-fallback`. GitHub signs the fallback job's OIDC token
with the dispatched ref, and that job runs pull-request and branch code, so
approval, dispatch and registration refuse the default branch and tags. Protect
the dedicated branch so only maintainers can change it, and never let a cloud
role, such as an AWS IAM trust policy or a GCP workload identity provider,
trust its ref.
Use the dispatch-only [fallback workflow](https://github.com/sparkwing-dev/sparkwing/blob/main/.github/workflows/sparkwing-fallback.yaml)
as the template. Set repository variables `SPARKWING_CONTROLLER` and
`SPARKWING_TEAM`. The template pins `SPARKWING_RUNNER_VERSION` to a release
that supports `--dispatch-receipt` and checks the release signature before it
runs the runner; bump the pin when the controller upgrades.
The workflow needs `run-name: sparkwing:${{ inputs.receipt }}`, inputs named
`receipt` and `runner`, `id-token: write` and `contents: read`. It invokes
`sparkwing-runner warm --github-actions --dispatch-receipt <receipt>`.

The template targets v0.71.0. Publish that release before creating or approving
the protected fallback ref. Published v0.70.0 runners lack `--dispatch-receipt`;
installing v0.70.0 cannot execute this workflow.
It has only `workflow_dispatch`, so pushes do not start fallback workers.

The App's repository permission must include **Actions: Read and write**.
Each installation owner approves the permission update in GitHub. Until approved,
the waiting-job notice names the installation approval link. Source-fetch tokens
keep Contents read only. Only dispatch and cancellation tokens carry Actions
write; registration reads run and job metadata with Actions read. These tokens
stay in the controller and are restricted to the numeric repository ID.

A receipt is a correlation value, not a bearer credential. Registration verifies
GitHub OIDC, workflow ref and SHA, numeric repository and owner, provider run and
attempt, and the OIDC job's provider-reported image label. Only a GitHub-hosted
runner can claim an explicit Actions tier. The resulting credential claims only
the receipt's original job, with its original source and trust. A default-branch
workflow cannot upgrade pull-request code to default-branch trust. Fork pull
requests are never dispatched, so they spend no GitHub minutes, and registration
refuses them. Each workflow attempt exchanges once.

Run status and the run page retain every dispatch receipt after award, cancellation
and completion, with its requested, accepted, refused, uncertain or cancelled state
and a provider link once registration identifies the run. Each job shows its
dispatch count and how many fallbacks lost the race to another worker. Cancellation
invalidates the receipt and cancels a known provider run. A receipt stays live
while its queue generation still waits, including after a returned award, and
while its own workflow holds the job. A job that workflow finishes leaves it to
exit with its real outcome; one that another runner takes cancels the idle workflow. GitHub answers that
the run is gone or already finished count as done; a removed, suspended or
refused installation is logged once for an operator and not retried. A lost response or sender
restart never resends the generation. An operator can make an audited retry with
`POST /api/v1/operator/teams/{team}/actions-dispatches/{receipt}/retry`, supplying
`{"reason":"provider recovery checked"}`. This invalidates the old receipt before
opening a new generation and refuses while a live award exists. A delayed old
workflow can still spend GitHub startup minutes.
