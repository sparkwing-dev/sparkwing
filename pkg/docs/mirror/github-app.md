# GitHub App

The Sparkwing GitHub App connects a team to the GitHub repositories it controls. An installation proves control: GitHub lets only an account's owner install an App on it, so a binding from an installation to a team is something a team member cannot claim for a repository they do not administer. With an installation bound, a team gets:

- push, pull request, release and branch runs from the App's webhook, for the pipelines the team subscribes to each repository;
- source for cloud runners through a short-lived installation token restricted to `contents: read` for the primary repository ID and the extra repository IDs a team owner approved ([extra repositories](git-credentials.md#extra-repositories));
- automatic controller cron arming from `on.schedule` declarations with `where: controller` on a pushed default branch after a team admin enables repository automation;
- a check run on the commit each of those runs builds, and a re-run when someone re-runs it from GitHub.

A deployment runs one App. Its private key stays in the controller, which mints every token it needs and hands out source tokens restricted to the approved repository-ID set.

## Operator configuration

The App's client id and secret are the ones GitHub sign-in uses, so sign-in and the App are one GitHub registration.

| Setting | Source |
| --- | --- |
| App id | `--github-app-id` |
| App slug (the `https://github.com/apps/<slug>` name) | `--github-app-slug` |
| Private key (PEM) | the `github-app-key` [credential](self-hosting.md#controller-credentials) |
| Webhook secret | the `github-app-webhook-secret` credential |
| Previous webhook secret, while a rotation settles | the optional `github-app-webhook-secret.previous` credential |
| Client id and secret | `--github-client-id` and the `github-client-secret` credential, shared with sign-in |
| Connect callback | `https://console.sparkwing.dev/github/app/callback`, added to `--oauth-redirect-uris` |

With the `sparkwing-full` chart, the flags are `controller.github.appID`, `controller.github.appSlug`, `controller.github.clientID` and `controller.oauthRedirectURIs`, and the key, webhook secret and client secret are keys of `controller.credentialsSecret`.

To rotate the webhook secret, keep the old one as `github-app-webhook-secret.previous`, put the new one in `github-app-webhook-secret`, restart, change the secret in the App's settings, then remove the previous one. Deliveries signed with either verify meanwhile. The controller never retires the previous secret on its own, so it stays valid until you delete the credential and restart. A delivery body may be up to 25 MiB, GitHub's own cap; a larger one is refused with HTTP 413. A delivery up to 1 MiB is verified in memory. A larger one spools to disk until its signature verifies, and at most four large deliveries spool at once; a fifth is answered with HTTP 503 and `Retry-After`. GitHub does not retry it, so redeliver it from the App's advanced settings.

The controller starts even when the App settings are incomplete, its private key is malformed, an App credential file is empty or unreadable, or GitHub OAuth credentials or redirect URIs are invalid. App features and GitHub sign-in stay unavailable until their configuration validates. App routes and App-driven work refuse with `GitHub App unavailable: <reason>`. `GET /api/v1/health` reports degraded health with that reason, and local `sparkwing doctor` reports the unavailable App. Fix the settings and restart the controller to enable it. Other authenticated controller features, including run intake and inherited child runs that do not need App authority, remain available. Webhook signature verification stays strict: unsigned deliveries and invalid signatures are rejected. `GET /api/v1/capabilities` reports `github_app.slug` when the App is available.

### GitHub App settings

The examples use the hosted deployment's hosts: the dashboard at `console.sparkwing.dev` and the API at `api.sparkwing.dev`. Both route to the controller, which serves the dashboard and the API on one listener. A self-hosted deployment puts its own dashboard URL (`--dashboard-url`) and controller external URL (`--external-url`) in their place, and may use one host for both.

- **Webhook**: active. URL `https://api.sparkwing.dev/webhooks/github-app`. Secret: the webhook secret above.
- **Setup URL**: `https://console.sparkwing.dev/github/app/setup`, with **Redirect on update** checked.
- **Callback URLs**: `https://console.sparkwing.dev/github/app/callback`, beside the sign-in callback.
- **Request user authorization (OAuth) during installation**: unchecked. The connect flow asks for the user's authorization as its own step.
- **Repository permissions**: Checks read and write, Contents read-only, Commit statuses read and write, Metadata read-only, Pull requests read-only. Commit statuses serve an installation until its owner accepts the Checks permission; see [Check runs](#check-runs).
- **Organization permissions**: Members read-only.
- **Account permissions**: Email addresses read-only (sign-in).
- **Subscribe to events**: Check run, Check suite, Push, Pull request, Release, Create, Delete, Repository. GitHub sends `installation` and `installation_repositories` to every App without a checkbox.

## Connecting a team

Only a team owner connects, and only as a signed-in account with a linked GitHub identity, which a Google-first account adds from **Account -> Linked sign-ins** ([linked sign-ins](auth.md#linked-sign-ins)). Unlinking that GitHub sign-in later leaves the installations the account connected bound to their teams. In the dashboard, the owner opens **Team -> GitHub** and chooses **Connect GitHub**; the tab appears when `GET /api/v1/capabilities` reports `github_app`. Readers and editors see the same tab read-only.

1. **Connect GitHub** posts to the controller's `/github/app/connect` with the session's CSRF token. The controller creates the install URL, the authorize URL, a state and a PKCE verifier, keeps the state and verifier in a short-lived `__Host-` cookie, and sends the browser to the install URL.
2. GitHub returns the browser to the setup URL, `/github/app/setup`, with `installation_id`, `setup_action` and `state`. The controller checks `state` against the cookie, keeps `installation_id` in the cookie, and sends the browser to the authorize URL.
3. GitHub returns the browser to the callback, `/github/app/callback`, with `code` and `state`. The controller checks `state` again, keeps the code in the cookie, and moves on to `/github/app/complete`, a same-site request that carries the session and binds the installation.

The `installation_id` the controller binds is the one step 2 recorded in the cookie, never one from the callback URL. The callback keeps GitHub's code in the flow cookie and resumes on the controller's own origin before the session is used. A setup return with `setup_action=request` means an organization owner must approve the install; the controller's page says so and the owner connects again after the approval.

When an administrator changes repository access on GitHub outside a connect flow,
GitHub returns to the setup URL with `setup_action=update`, and the controller
opens **Team → GitHub** with “Repository access updated on GitHub”.

The controller binds the installation to the caller's active team only when all of these hold:

- `state` carries the controller's signature, has not expired (ten minutes), names the caller's account and active team, and was not used before. Used states are kept in the store until they expire, so no replica and no restart finishes a flow twice;
- `verifier` hashes to the value `state` carries, which only the browser that started the flow holds;
- the code redeems, with that verifier, for a GitHub user whose id equals the GitHub identity linked to the caller's account;
- GitHub's record of the installation, read with the App's own credential, names an account that user administers: the user's own account, or an organization where the user's membership is `admin`.

The last rule is stricter than "the user can see the installation" on purpose. An organization member with read access to one repository sees the organization's installation in `GET /user/installations`, and a binding would give their team source tokens for every repository in it.

An installation belongs to one team. A freshly verified current GitHub account owner or organization admin can reconnect it to their active team. The transaction removes the former team's subscriptions and automation consent, withdraws its schedules, and binds the installation to the new team. Both teams receive a `github.installation_reclaimed` audit event naming the actor, installation, previous team and new team. The controller emails the previous team's owners at their verified addresses. Without a configured mail service it logs the notification attempt. Reconnecting one the team already holds refreshes it.

### Connecting an App installed on GitHub already

On **Team -> GitHub**, choose **Already installed the app? Connect an existing installation**. Sparkwing sends the browser straight to GitHub's authorization page. After GitHub returns, Sparkwing lists installations of this App that the linked GitHub user administers: the user's account and organizations where their membership is active and has the `admin` role. The list carries no binding state: an installation another team holds appears like any other. Selecting it repeats the current admin check before transferring the binding. If the list is empty, use **Connect GitHub** to install the App.

Choosing an installation binds it to the active team. The controller checks the signed, unexpired state and PKCE verifier against the account and team, confirms the linked GitHub identity, and consumes the state before checking the selected installation. The encrypted authorization proof carries the IDs shown in the list, so an ID absent from that list answers 404 and cannot bind. For a listed ID, the controller reads the installation with the App credential and repeats the admin check. A selection with valid state, proof and linked identity consumes the state, including across controller replicas; retry by starting a new connection flow.

An installation stops being bound when GitHub reports it deleted, when a team owner calls `DELETE /api/v1/team/github-app/installations/{installation_id}`, or when the operator calls `DELETE /api/v1/operator/github-app/installations/{installation_id}`. A verified current administrator can also reclaim it through a new connection flow. Unbinding does not uninstall the App from GitHub.

## Runs from GitHub events

### Repository-managed triggers

In **Team → GitHub**, select a connected repository, review its declared pipelines,
and choose **Enable declarations**. Connecting an installation alone does not enable
repository automation. The controller reads `.sparkwing/sparkwing.yaml` at the
verified default-branch head without compiling or executing repository Go code.
Subsequent signed deliveries use those declarations; execution stays pinned to the
event's commit. Feature-branch and PR-head configuration cannot authorize a run.

`GET /api/v1/team/github-app/automation?repository=owner/name` previews the
configuration, source SHA, default branch, consent state and pipeline filters.
An owner enables it with `PUT /api/v1/team/github-app/automation {repository, runner}`
and revokes it with `DELETE /api/v1/team/github-app/automation?repository_id=<id>`. App schedule arming and each due fire check that consent in the committing transaction. Revoking it prevents later fires.
`runner` selects who owns the repository's automatic runs and its App-armed
schedules; see [run owner](#run-owner).
GET without a repository lists enabled repositories. Discovery is read-only.

Supported declarations are `on.push.branches` and
`on.pull_request.actions`/`branches` (the PR base branch). Empty branch filters
match all branches. Empty PR actions select `opened`, `synchronize` and `reopened`;
`closed` and `ready_for_review` are also supported. Patterns use Go `path.Match`:
`*` does not cross `/`. Recursive `**`, negative patterns, changed-path filters
and unsupported PR actions produce an explicit unsupported status instead of
broader execution. Other event types remain configured through manual subscriptions.

Missing, invalid, unsupported or unreadable configuration starts no automatic
runs. The preview explains the problem; refresh discovery after fixing it.
Temporary discovery failures preserve saved consent. Removing repository access or
disconnecting its installation revokes consent; reconnecting requires enabling it
again. Existing manual subscriptions take precedence for their pipeline name,
including events their filters exclude. The preview marks these manual overrides.

### Run owner

Automation and every manual subscription take `runner`, which selects the owner
of the runs they start:

| `runner` | Owner |
| --- | --- |
| `anywhere` (default) | Any eligible claimant, ordered by the team's claim ranking. Personal machines never take App runs |
| `cloud` | Sparkwing Cloud's launcher plans and runs the work |
| `github-actions` | Only a GitHub Actions runner bound to the delivery's push claims the run's trigger, as in [GitHub Actions runners](github-actions-runners.md) |
| `pool=<name>` | A self-hosted runner reserved for that pool; a pool access rule must still allow the repository. App-armed schedules qualify for the rule through the repository's automation consent |

The selection is read from the saved configuration at intake and sealed in the
run's request; webhook payloads cannot change it. Retries keep it. Changing
`runner` affects only runs admitted afterwards.

### Manual subscriptions

The **Advanced** section retains explicit subscriptions. A team subscribes a pipeline to a repository with `PUT /api/v1/team/github-app/triggers {repository, pipeline, push, tags, release_tags, pull_request, branches, base_branches, runner, ...}`. `push` selects branch pushes; `tags` is a list of tag name glob patterns, such as `["v*"]`, that selects tag pushes; `pull_request` selects pull request `opened`, `synchronize` and `reopened` events. An empty `tags` list selects no tag pushes. At least one event must be selected. Patterns use Go `path.Match` semantics: `*` does not cross `/`, so `v*` matches `v1.2.3` but not `v1/nested`. A subscription accepts at most 10 patterns of up to 128 bytes each in `tags` and in `release_tags`. GitHub does not protect tags by default. Configure GitHub tag protection rules for release tags before subscribing a release pipeline. The repository must be in one of the team's installations when the subscription is written. The pipeline is named explicitly. Each additional option defaults to `false`:

| Option | Event |
| --- | --- |
| `pull_request_closed` | PR closed after a merge; the run executes the merge commit, not the head, as `refs/heads/<base>`. A PR closed without merging starts nothing |
| `pull_request_labeled` with nonempty `pull_request_labels: ["ship", ...]` | PR labeled with one of the listed labels, compared without case |
| `pull_request_ready_for_review` | Draft PR marked ready |
| `release_published`, `release_prereleased` with nonempty `release_tags` | Release action at its tag's commit, only when the tag matches a `release_tags` pattern. A release subscription without `release_tags` is refused with 400 |
| `branch_create`, `branch_delete` | Branch creation or deletion; deletion runs at the current commit of the delivery's `repository.default_branch` |

`branches` filters branch pushes and branch creation and deletion by the branch name; it does not apply to tag pushes, which `tags` alone selects, or to releases, which `release_tags` alone selects. Anyone who can push a tag can publish a release on it, so protect the tags both lists name. `base_branches` filters every pull request event by the pull request's base branch. Both are arrays of up to 10 glob patterns, each at most 128 bytes, matched with Go `path.Match` against the branch name. A pattern or branch may carry `refs/heads/`: `main` and `refs/heads/main` select the same branch. An empty list matches every branch, including for subscriptions written before these fields existed. For example, `{"repository":"acme/widgets","pipeline":"deploy","push":true,"branches":["main","release/*"]}` runs deploy on `main` or a one-level `release/` branch. Set `branches` on every deploy pipeline subscribed to push or branch creation so feature branches cannot deploy. The controller applies these filters before creating a run; pipeline YAML in the pushed commit cannot change them.

The subscription follows GitHub's repository id across a rename. A `repository` `renamed` or `transferred` delivery updates its displayed name and installation when the new installation belongs to the same team and covers the repository. No pipeline runs from the repository event itself.

`POST /webhooks/github-app` verifies `X-Hub-Signature-256` with the App's webhook secret and answers 401 for a signature that does not verify. It stores the delivery ID, event and signed body before answering 202 with status `queued`. No GitHub API call runs on ingress. A worker leases queued deliveries and routes the payload's `installation.id` to the bound team. Unbound or suspended installations start nothing. For each subscribed event the worker creates one trigger per pipeline, recording the branch or tag ref, commit, repository and installation ID. It resolves release tags and branch refs through a repository-restricted installation token before dispatch. Any processing failure marks the delivery failed with its reason. Deliveries never retry automatically.

Runs expose `GITHUB_EVENT_NAME`, `GITHUB_REF`, `GITHUB_REF_TYPE` and `GITHUB_ACTION`. Tag push triggers carry `GITHUB_REF=refs/tags/<tag>`, `GITHUB_REF_TYPE=tag` and `GITHUB_TAG=<tag>`, and a tag node also gets `GITHUB_REF_NAME=<tag>`. Branch push triggers carry `GITHUB_REF=refs/heads/<branch>` and `GITHUB_REF_TYPE=branch`. PR runs also expose `GITHUB_LABEL` and `GITHUB_MERGED` (`true` or `false`), along with the existing `GITHUB_PR_*` fields. Release runs expose `GITHUB_TAG_NAME`. PR refs are `refs/pull/<number>/head`, except a merged PR's `closed` run, whose ref is `refs/heads/<base>`; release refs are `refs/tags/<tag>`; branch events expose `refs/heads/<branch>`, including the deleted branch. OIDC trigger claims use `push`, `pull_request`, `release`, `create` or `delete` with those refs.

The worker starts nothing when:

- it is a pull request from a fork (see below);
- it is a push of no commit: a deleted branch or tag, or an `after` of all zeros;
- no subscribed pipeline matches the push branch, pushed tag (`tags`), release tag (`release_tags`) or pull request base branch;
- it closes a pull request without merging it, or names no merge commit for a merged one;
- the event is older than the team's binding of the installation, going by the push's `repository.pushed_at` or the pull request's `updated_at`, so an event meant for the installation's previous team does not run in the next one;
- the event carries no such time, or one the controller cannot read, since nothing then shows it is not older than the binding; release uses `published_at`, and branch events use the repository's `pushed_at` or `updated_at`;
- GitHub no longer reports the installation as covering the repository. The controller asks GitHub on every delivery that would start a run, never from a cached answer.

The inbox binds every accepted delivery ID and signed body's digest. Replaying either acknowledges the existing delivery without starting another copy. Completed and failed delivery bindings expire 90 days after receipt. Completion releases the body. Failed deliveries keep their body until then for manual processing. Pending deliveries retain their body until processing completes. Each targeted admission, schedule publication, withdrawal and lifecycle change commits with a receipt containing its result. Manual processing returns committed run identities and publication results without applying those effects again. Each pipeline has its own admission receipt, so a partially processed delivery can finish its remaining work without spending the completed pipelines’ budget again. A run-starting event already older than 90 days when it reaches the inbox is refused during processing and logged. Time spent in the queue does not age out an accepted event. Event time comes from the signed body's `repository.pushed_at` for push, `create` and `delete` (or `repository.updated_at` when later), `release.published_at` for releases, and `pull_request.updated_at` for pull requests.

The worker rotates across teams with ready deliveries and takes the oldest ready delivery within each team. The signed installation ID determines the queue's team at receipt. The delivery stays bound to that installation connection. Moving an installation away and back, or disconnecting and reconnecting it in the same team, cannot revive deliveries from the former connection. Deliveries without a bound installation share one fallback queue. A team's backlog cannot take another team's turn.

GitHub signs the body but not the `X-GitHub-Event` header, so the controller also binds every verified body to the event it first arrived as, whether or not it started anything, and keeps that binding for the same 90 days. The same body sent again under another event name answers 409 and is not read. An `installation` event must name this App's id in its installation object, which only installation events carry.

Each run a delivery creates spends one of the team's hourly runs (`--max-runs-per-principal-hour`, see [security](security.md)), from the same budget the team's API submissions spend. When placement policy requires paid execution and credits are exhausted or held, the push records a run that did not start, visible in the run list. Policy that permits an unmetered runner queues the run even when no such runner has registered or every matching runner is busy. Runner labels and current availability do not change this funding decision. After funding the team, use Run now to start it. Pipelines already started remain intact. Processing failures appear under **Pushes we couldn’t process** in the team’s GitHub settings. Team admins can inspect the reason and choose **Process again**. The API exposes the same admin-only, team-scoped operations at `GET /api/v1/team/github-app/deliveries/failed` and `POST /api/v1/team/github-app/deliveries/{delivery}/process-again`. Each manual action writes an audit event.

The hourly run budget uses store-wide counters shared by controller replicas.

`installation` deliveries keep the binding current: `deleted` unbinds, `suspend` and `unsuspend` mark it. The repositories an installation covers are read from GitHub when they matter, so adding or removing a repository on GitHub takes effect on the next delivery. Repository listing reads every page, including installations covering more than 1,000 repositories. The full listing, including token minting, has a one-minute deadline. Repeated repository IDs or a listing that stops before GitHub's reported total fail explicitly. The source credential route mints directly against the approved repository IDs.

If GitHub's repository listing fails during a repository-removal delivery, Sparkwing withdraws automation consent and schedules only for the positive repository IDs in `repositories_removed`. Unrelated repositories keep their schedules and consent. If the payload names no repositories or contains an invalid ID, Sparkwing preserves them all and records the delivery as failed for the admin's Process again action. A successful listing reconciles current coverage, so a late removal for a repository GitHub lists as covered leaves its schedules active.

Schedule publication captures the repository's publication generation and original
consent before fetching source. Its transaction checks both, then replaces the
schedule set, advances the generation and records the result together.
Withdrawals and consent changes advance the generation too. A worker holding an
older source snapshot cannot restore declarations after a newer publication or
withdrawal. Installation-wide withdrawal records its original repository targets
and aggregate result in the same transaction. Reprocessing that delivery cannot
withdraw schedules or consent added later, even if the installation listing
changes between attempts. These checks live in the store and apply across workers.
Disconnecting the installation during source preparation revokes the pending
admission and publication. It cannot start a run or change schedules.

A run an App-armed schedule starts seals its repository as `github.com/owner/name`, the same display a push run carries. Once a due instant commits its run, retrying that instant replays the original sealed revision even if the branch moves or GitHub is unavailable. Replay still checks the team's current authority. Only a team's own installations withdraw its App-armed schedules. Replacement applies only to the same installation and repository. A member replacing their own repository schedules cannot withdraw App schedules. When a repository moves to an installation another team binds, the former team's schedules stop when its own installation reports the removal, transfer or deletion.

### Pull requests from forks

A pull request whose head repository is not its base repository, or whose head repository was deleted, is not run. The webhook refuses it, and seals the head repository GitHub reports into every pull request run so intake refuses a fork again on its own. The controller logs it and acknowledges the delivery as ignored; no subscription setting changes this. A fork's code would run on the team's runner with the runner's token in reach, and that token can claim the team's other work and read its secrets. If your team needs pull requests from forks built, contact support.

## Source for cloud runners

Cloud source fetches require a real `.sparkwing` directory in the checkout.
A symlink at `.sparkwing`, the repository's `go.mod`, or
`.sparkwing/go.mod` fails the source fetch before private modules are
downloaded or the pipeline is built. Put pipeline files and module manifests
in the repository itself.

Source acquisition uses `POST /api/v1/runs/{run}/source-credential` under a
live claim with `source.read` and the sealed source binding. Repository identity
comes from intake. A typed repository name cannot grant access to an App's private
repositories. App issuance checks the current installation and the authorized
repository IDs, including any owner-approved extra repositories. Both source
and extra approvals follow numeric IDs through renames. The release audit
commits before the token is returned in the response's `secret` field. GitHub
minting runs outside database transactions. A second short transaction rechecks
the installation, subscription or schedule consent, approved IDs and claim fence.
GitHub refusing the approved ID set returns 409 and names that set. Other mint
failures return 503. No token is returned after failed reauthorization or audit.
App tokens and stored team credentials require the executor's repository match
and either a same-team shared runner or verified Cloud placement. A personal
machine never receives either.
The store records each release before exposing a credential. The source owner
passes credentials through memory or private fetch configuration, applies the
shared Git environment policy and removes acquisition-owned files after use.
See [team git credentials](git-credentials.md).

Native source acquisition and the transactional release binding still need
integration evidence. Controller revocation blocks later releases, while GitHub
tokens already issued retain their upstream lifetime.

## Check runs

Each run the App started reports as one check run named `sparkwing/<pipeline>` on the commit it builds: the pushed commit, or a pull request's head. Only runs whose trigger the App webhook created report this way; runs from the CLI, the API or a schedule report nothing to GitHub.

The check run is created `queued` when the run is dispatched, moves to `in_progress` when a runner starts it, and ends `completed` with one of these conclusions:

| Run outcome | Conclusion |
| --- | --- |
| `success` | `success` |
| `failed` | `failure` |
| `cancelled` | `cancelled` |
| no runner claimed it before the queue deadline | `timed_out` |

`details_url` links to the run in the dashboard (`--dashboard-url`) and `external_id` is the run id. A completed check run's summary gives only the outcome, duration, counts of nodes by outcome, and a link to the signed-in Sparkwing console. Node names, errors and logs stay on the run page. The controller records the check run's id with the run, so every later update edits the same check run. Each write holds a per-run lock in the state database, so two controllers sharing it during an upgrade create one check run between them. Before creating a check run the controller looks for one of its own on the commit whose `external_id` is the run id and adopts it, so a create whose record was lost is not repeated. An update behind the run's stored state (queued, then running, then finished) is dropped, so a check run or commit status never moves back and a finished one keeps its conclusion.

The controller writes check runs in the background with an installation token restricted to the run's numeric repository ID and `checks: write`. It resolves the repository's current name before reporting, so renames keep working. Transient write failures are logged and remain pending for retry across controller restarts. Invalid committed App facts are recorded once and consume that revision without sending a check. GitHub failing a write never fails or delays the run. If the run advances before a report is delivered, the controller may report the newer phase directly. A valid terminal report stays pending until delivered.

An installation whose owner has not yet accepted the Checks permission gets commit statuses instead: `pending`, then `success`, `failure` or `error`, under the context `sparkwing/<pipeline>`, with a token restricted to `statuses: write`. The controller logs this once per installation. A run that started on commit statuses finishes on them, including after a controller restart. New runs after the owner accepts get check runs.

### Re-running from GitHub

GitHub's **Re-run** buttons start runs:

- `check_run` `rerequested` runs that check run's pipeline again. The check run must be this App's, and its `external_id` must name a run of the installation's team on the same repository id and commit.
- `check_suite` `rerequested` runs each subscribed pipeline with its own prior App run on that repository id and commit.

A re-run copies the branch and pull request from that pipeline's own prior run. The pipeline must still subscribe to that run's event, push or pull request, and its branch filter must still match. A commit the team never ran starts nothing, and so does a check run or suite whose pull requests include one from a fork. Older runs without a recorded GitHub repository id cannot be re-run from GitHub. Otherwise a re-run follows the push rules: the installation must be bound to a team and not suspended, GitHub must still report it covers the repository, each run spends one of the team's hourly runs, and a delivery whose signed body was processed before answers `queued`.

`check_suite` `requested` starts nothing: GitHub sends it for every push, and the push delivery already starts that commit's runs. `check_run` `created` and `completed`, which GitHub sends for the controller's own writes, and every other `check_run` and `check_suite` action start nothing either.

## GitHub Actions runners

A [GitHub Actions runner](github-actions-runners.md) credential does not create triggers. With the App installed, the push that starts the workflow also reaches the App webhook, which creates the trigger for that branch and commit, and the job's credential claims exactly that trigger. A credential that could create its own trigger would add a second way to start a run in the team, proven only by a binding the owner typed, when the installation already proves more.
