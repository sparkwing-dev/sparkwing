# GitHub App

The Sparkwing GitHub App connects a team to the GitHub repositories it controls. An installation proves control: GitHub lets only an account's owner install an App on it, so a binding from an installation to a team is something a team member cannot claim for a repository they do not administer. With an installation bound, a team gets:

- push, pull request, release and branch runs from the App's webhook, for the pipelines the team subscribes to each repository;
- source for cloud runners through a short-lived installation token restricted to one repository and `contents: read`, plus the repositories a team owner listed for it ([extra repositories](git-credentials.md#extra-repositories));
- automatic controller cron arming from `on.schedule` declarations with `where: controller` on a pushed default branch, with no separate Team switch;
- a check run on the commit each of those runs builds, and a re-run when someone re-runs it from GitHub.

A deployment runs one App. Its private key stays in the controller, which mints every token it needs and hands out only tokens restricted to a single repository.

## Operator configuration

The App's client id and secret are the ones GitHub sign-in uses, so sign-in and the App are one GitHub registration.

| Setting | Source |
| --- | --- |
| App id | `--github-app-id` or `SPARKWING_GITHUB_APP_ID` |
| App slug (the `https://github.com/apps/<slug>` name) | `--github-app-slug` or `SPARKWING_GITHUB_APP_SLUG` |
| Private key (PEM) | `SPARKWING_GITHUB_APP_PRIVATE_KEY_FILE` (a path) or `SPARKWING_GITHUB_APP_PRIVATE_KEY` (the PEM text); never a flag |
| Webhook secret | `SPARKWING_GITHUB_APP_WEBHOOK_SECRET`; never a flag |
| Client id and secret | `--github-client-id` and `SPARKWING_GITHUB_CLIENT_SECRET`, shared with sign-in |
| Connect callback | `https://console.sparkwing.dev/github/app/callback`, added to `--oauth-redirect-uris` |

The controller refuses to start with some of the App settings and not the others. `GET /api/v1/capabilities` reports `github_app.slug` when the App is configured.

### GitHub App settings

The examples use the hosted deployment's hosts: the dashboard at `console.sparkwing.dev` and the controller at `api.sparkwing.dev`. A self-hosted deployment puts its own dashboard and controller external URL in their place.

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

1. The dashboard calls `POST /api/v1/team/github-app/connect {redirect_uri}` and gets `{install_url, authorize_url, state, verifier}`. It keeps `state` and `verifier` in a short-lived `__Host-` cookie and sends the browser to `install_url`.
2. GitHub returns the browser to the setup URL with `installation_id`, `setup_action` and `state`. The dashboard checks `state` against its cookie, keeps `installation_id` in the cookie, and sends the browser to `authorize_url`.
3. GitHub returns the browser to the callback with `code` and `state`. The dashboard checks `state` again and calls `POST /api/v1/team/github-app/connect/complete {state, verifier, code, installation_id, redirect_uri}`.

The `installation_id` the dashboard sends is the one step 2 recorded in the cookie, never one from the callback URL. The callback keeps GitHub's code in the flow cookie and resumes on the dashboard origin before `connect/complete` uses the session. A setup return with `setup_action=request` means an organization owner must approve the install; the dashboard says so and the owner connects again after the approval.

When an administrator changes repository access on GitHub outside a connect flow,
GitHub returns to the setup URL with `setup_action=update`. The dashboard serves
a same-origin navigation page so the next request carries the Strict session
cookie, then opens **Team → GitHub** with “Repository access updated on GitHub”.

The controller binds the installation to the caller's active team only when all of these hold:

- `state` carries the controller's signature, has not expired (ten minutes), names the caller's account and active team, and was not used before. Used states are kept in the store until they expire, so no replica and no restart finishes a flow twice;
- `verifier` hashes to the value `state` carries, which only the browser that started the flow holds;
- the code redeems, with that verifier, for a GitHub user whose id equals the GitHub identity linked to the caller's account;
- GitHub's record of the installation, read with the App's own credential, names an account that user administers: the user's own account, or an organization where the user's membership is `admin`.

The last rule is stricter than "the user can see the installation" on purpose. An organization member with read access to one repository sees the organization's installation in `GET /user/installations`, and a binding would give their team source tokens for every repository in it.

An installation belongs to one team. Connecting one bound to another team answers 409 without naming the team. Reconnecting one the team already holds refreshes it.

### Connecting an App installed on GitHub already

On **Team -> GitHub**, choose **Already installed the app? Connect an existing installation**. Sparkwing sends the browser straight to GitHub's authorization page. After GitHub returns, Sparkwing lists installations of this App that the linked GitHub user administers: the user's account and organizations where their membership is active and has the `admin` role. An installation held by another team says **Connected to another team** without naming the team and cannot be selected. If the list is empty, use **Connect GitHub** to install the App.

Choosing an installation binds it to the active team. The controller checks the signed, unexpired state and PKCE verifier against the account and team, confirms the linked GitHub identity, and consumes the state before checking the selected installation. The encrypted authorization proof carries the IDs shown in the list, so an ID absent from that list answers 404 and cannot bind. For a listed ID, the controller reads the installation with the App credential and repeats the admin check. A selection with valid state, proof and linked identity consumes the state, including across controller replicas; retry by starting a new connection flow.

An installation stops being bound when GitHub reports it deleted, when a team owner calls `DELETE /api/v1/team/github-app/installations/{installation_id}`, or when the operator calls `DELETE /api/v1/github-app/installations/{installation_id}`, which is how a binding moves to another team. Unbinding does not uninstall the App from GitHub.

## Runs from GitHub events

A team subscribes a pipeline to a repository with `PUT /api/v1/team/github-app/triggers {repository, pipeline, push, tags, pull_request, branches, base_branches, ...}`. `push` selects branch pushes; `tags` is a list of tag name glob patterns, such as `["v*"]`; `pull_request` selects pull request `opened`, `synchronize` and `reopened` events. An empty `tags` list selects no tags. At least one event must be selected. Patterns use Go `path.Match` semantics: `*` does not cross `/`, so `v*` matches `v1.2.3` but not `v1/nested`. A subscription accepts at most 10 tag patterns of up to 128 bytes each. GitHub does not protect tags by default. Configure GitHub tag protection rules for release tags before subscribing a release pipeline. The repository must be in one of the team's installations when the subscription is written. The pipeline is named explicitly, the way `POST /webhooks/github/{pipeline}` names it in its URL: the controller does not read a repository's `on:` block. Each additional option defaults to `false`:

| Option | Event |
| --- | --- |
| `pull_request_closed` | PR closed, whether merged or not |
| `pull_request_labeled` with nonempty `pull_request_labels: ["ship", ...]` | PR labeled with one of the listed labels, compared without case |
| `pull_request_ready_for_review` | Draft PR marked ready |
| `release_published`, `release_prereleased` | Release action at its tag's commit |
| `branch_create`, `branch_delete` | Branch creation or deletion; deletion runs at the current default branch commit |

`branches` filters branch pushes and branch creation and deletion by the branch name; it does not apply to tag pushes, which `tags` alone selects, or to releases. `base_branches` filters every pull request event by the pull request's base branch. Both are arrays of up to 10 glob patterns, each at most 128 bytes, matched with Go `path.Match` against the branch name. An empty list matches every branch, including for subscriptions written before these fields existed. For example, `{"repository":"acme/widgets","pipeline":"deploy","push":true,"branches":["main","release/*"]}` runs deploy on `main` or a one-level `release/` branch. Set `branches` on every deploy pipeline subscribed to push or branch creation so feature branches cannot deploy. The controller applies these filters before creating a run; pipeline YAML in the pushed commit cannot change them.

The subscription follows GitHub's repository id across a rename. A `repository` `renamed` or `transferred` delivery updates its displayed name and installation when the new installation belongs to the same team and covers the repository. No pipeline runs from the repository event itself.

`POST /webhooks/github-app` verifies `X-Hub-Signature-256` with the App's webhook secret and answers 401 for a signature that does not verify. It routes by the payload's `installation.id` to the bound team; a delivery for an unbound or suspended installation is acknowledged and does nothing. For each subscribed event it creates one trigger in that team per pipeline, recording the branch or tag ref, commit, repository and installation id. The controller resolves release tags and branch refs through a repository-restricted installation token before dispatch. The operator webhook at `POST /webhooks/github/{pipeline}` acknowledges tag pushes without starting runs.

Runs expose `GITHUB_EVENT_NAME`, `GITHUB_REF`, `GITHUB_REF_TYPE` and `GITHUB_ACTION`. Tag push triggers carry `GITHUB_REF=refs/tags/<tag>`, `GITHUB_REF_TYPE=tag` and `GITHUB_TAG=<tag>`, and a tag node also gets `GITHUB_REF_NAME=<tag>`. Branch push triggers carry `GITHUB_REF=refs/heads/<branch>` and `GITHUB_REF_TYPE=branch`. PR runs also expose `GITHUB_LABEL` and `GITHUB_MERGED` (`true` or `false`), along with the existing `GITHUB_PR_*` fields. Release runs expose `GITHUB_TAG_NAME`. PR refs are `refs/pull/<number>/head`; release refs are `refs/tags/<tag>`; branch events expose `refs/heads/<branch>`, including the deleted branch. OIDC trigger claims use `push`, `pull_request`, `release`, `create` or `delete` with those refs.

A delivery starts nothing, and is acknowledged with the reason, when:

- it is a pull request from a fork (see below);
- it is a push of no commit: a deleted branch or tag, or an `after` of all zeros;
- no subscribed pipeline matches the push branch, pushed tag or pull request base branch;
- the event is older than the team's binding of the installation, going by the push's `repository.pushed_at` or the pull request's `updated_at`, so an event meant for the installation's previous team does not run in the next one;
- the event carries no such time, or one the controller cannot read, since nothing then shows it is not older than the binding; release uses `published_at`, and branch events use the repository's `pushed_at` or `updated_at`;
- GitHub no longer reports the installation as covering the repository. The controller asks GitHub on every delivery that would start a run, never from a cached answer.

The controller remembers the digest of every signed body that started runs, for 90 days and for every team. A delivery with the same body answers 200 with status `duplicate` and the runs it started in the current team, before it is counted against any cap, so GitHub's redelivery and a replay after the installation moves teams start nothing. Within a team, each trigger's replay key (a digest of the pipeline and the signed body) and delivery key (the delivery id and pipeline) still refuse a second copy of a run.

Each run a delivery creates spends one of the team's hourly runs (`--max-runs-per-principal-hour`, see [security](security.md)), from the same budget the team's API submissions spend. When the budget runs out before the first run, the delivery answers 429 with `Retry-After`. When it runs out partway, the runs already created stand, the rest are listed with status `shed`, and the delivery is not remembered. A redelivery answers the runs it already started as `duplicate` without spending anything, and spends the budget only on the runs that were shed.

The hourly budget is enforced separately by each controller replica.

`installation` deliveries keep the binding current: `deleted` unbinds, `suspend` and `unsuspend` mark it. The repositories an installation covers are read from GitHub when they matter, not stored, so adding or removing a repository on GitHub takes effect on the next delivery. The source-token route, which serves a run that already exists, keeps GitHub's answer for up to a minute, and an `installation_repositories` delivery drops what it kept.

### Pull requests from forks

A pull request whose head repository is not its base repository, or whose head repository was deleted, is not run. The controller logs it and acknowledges the delivery as ignored; no subscription setting changes this. A fork's code would run on the team's runner with the runner's token in reach, and that token can claim the team's other work and read its secrets. If your team needs pull requests from forks built, contact support.

## Source for cloud runners

A runner without the git cache asks `POST /api/v1/runs/{id}/git-credential` before it fetches a run's source. The controller answers a caller that holds a live claim on the run, with a token of the run's own team, and resolves the credential in a fixed order:

1. When the run's repository is on github.com and an installation bound to the run's team, and not suspended, covers it, the answer is `{kind: "github_app", host, token, expires_at, repository, extra_repositories}`: an installation token restricted to that repository, and to the extra repositories a team owner listed for it, with `contents: read`. GitHub issues it for at most an hour. The route reads nothing from the request body, so neither the runner nor anything in the fetched tree widens the token.
2. Otherwise, when the team stored a git credential for the host of the run's repository, the controller releases it; see [Team git credentials](git-credentials.md).
3. Otherwise it answers 404 with `{"error": "no_source_credential"}` and a message naming both remedies: install the App on the repository, or store a git credential for the host.

The caller gets 403 when it holds no claim on the run. The runner never falls back to credentials of the machine it runs on; only a runner its owner fenced with `--allow-repo` does, and only when the controller releases nothing.

`POST /api/v1/runs/{id}/source-token` is the older route, which serves only the App token as `{token, expires_at, repository}`, and 404 when the repository has no bound installation. A runner asks it when the controller answers the git-credential route with a plain 404, as one from before that route does.

A claim holds one live token. Asking again returns the same token until five minutes before it expires, when the controller mints the next one, and a claim that asks more than ten times in a minute answers 429 with `Retry-After`. A runner in a retry loop therefore neither mints a stream of tokens nor spends the App's GitHub rate limit.

The fetch inherits the token on a pipe, and a credential helper scoped to `https://github.com/`, set in the fetch's environment, reads it from there when GitHub asks. A fetch that presents a released credential reads no system, global or environment git config, so no helper, `http.extraHeader` or `insteadOf` rule of the machine takes part, and it runs without the machine's ssh agent. The token is never in an environment variable, the URL, a command line, a file or a log line; the fetch runs without `GIT_TRACE*` and `GIT_CURL_VERBOSE`, which would write it to a trace; and the checkout's own git commands see neither the helper nor the pipe. While the fetch runs, the token is in the memory of git's own processes, which the runner's user can read like any of its processes.

## Check runs

Each run the App started reports as one check run named `sparkwing/<pipeline>` on the commit it builds: the pushed commit, or a pull request's head. Only runs whose trigger the App webhook created report this way; the operator's `GITHUB_TOKEN` reporter keeps posting commit statuses for operator webhook bindings, unchanged.

The check run is created `queued` when the run is dispatched, moves to `in_progress` when a runner starts it, and ends `completed` with one of these conclusions:

| Run outcome | Conclusion |
| --- | --- |
| `success` | `success` |
| `failed` | `failure` |
| `cancelled` | `cancelled` |
| no runner claimed it before the queue deadline | `timed_out` |

`details_url` links to the run in the dashboard (`--dashboard-url`) and `external_id` is the run id. A completed check run's summary gives only the outcome, duration, counts of nodes by outcome, and a link to the signed-in Sparkwing console. Node names, errors and logs stay on the run page. The controller records the check run's id with the run, so every later update edits the same check run.

The controller writes check runs in the background with an installation token restricted to the run's repository and `checks: write`. GitHub failing a write never fails or delays the run: a write GitHub answers with 429 or a 5xx, or does not answer, is tried up to four times with waits of 0.25, 0.5 and 1 seconds, each failure is logged at warn, and a write that still fails is dropped. The next state of the run is written as usual; when the check run was never created, that write creates it.

An installation whose owner has not yet accepted the Checks permission gets commit statuses instead: `pending`, then `success`, `failure` or `error`, under the context `sparkwing/<pipeline>`, with a token restricted to `statuses: write`. The controller logs this once per installation. A run that started on commit statuses finishes on them, and runs after the owner accepts get check runs.

### Re-running from GitHub

GitHub's **Re-run** buttons start runs:

- `check_run` `rerequested` runs that check run's pipeline again. The check run must be this App's, and its `external_id` must name a run of the installation's team on the same repository id and commit.
- `check_suite` `rerequested` runs each subscribed pipeline with its own prior App run on that repository id and commit.

A re-run copies the branch and pull request from that pipeline's own prior run. The pipeline must still subscribe to that run's event, push or pull request, and its branch filter must still match. A commit the team never ran starts nothing, and so does a check run or suite whose pull requests include one from a fork. Older runs without a recorded GitHub repository id cannot be re-run from GitHub. Otherwise a re-run follows the push rules: the installation must be bound to a team and not suspended, GitHub must still report it covers the repository, each run spends one of the team's hourly runs, and a delivery whose signed body was processed before answers `duplicate`.

`check_suite` `requested` starts nothing: GitHub sends it for every push, and the push delivery already starts that commit's runs. `check_run` `created` and `completed`, which GitHub sends for the controller's own writes, and every other `check_run` and `check_suite` action start nothing either.

## GitHub Actions runners

A [GitHub Actions runner](github-actions-runners.md) credential does not create triggers. With the App installed, the push that starts the workflow also reaches the App webhook, which creates the trigger for that branch and commit, and the job's credential claims exactly that trigger. A credential that could create its own trigger would add a second way to start a run in the team, proven only by a binding the owner typed, when the installation already proves more.
