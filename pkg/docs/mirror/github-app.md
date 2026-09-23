# GitHub App

The Sparkwing GitHub App connects a team to the GitHub repositories it controls. An installation proves control: GitHub lets only an account's owner install an App on it, so a binding from an installation to a team is something a team member cannot claim for a repository they do not administer. With an installation bound, a team gets:

- push and pull request runs from the App's webhook, for the pipelines the team subscribes to each repository;
- source for cloud runners through a short-lived installation token restricted to one repository and `contents: read`;
- commit statuses on the commits those runs build.

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
| Connect callback | `https://<dashboard>/github/app/callback`, added to `--oauth-redirect-uris` |

The controller refuses to start with some of the App settings and not the others. `GET /api/v1/capabilities` reports `github_app.slug` when the App is configured.

### GitHub App settings

- **Webhook**: active. URL `https://<controller external URL>/webhooks/github-app`. Secret: the webhook secret above.
- **Setup URL**: `https://<dashboard>/github/app/setup`, with **Redirect on update** checked.
- **Callback URLs**: `https://<dashboard>/github/app/callback`, beside the sign-in callback.
- **Request user authorization (OAuth) during installation**: unchecked. The connect flow asks for the user's authorization as its own step.
- **Repository permissions**: Contents read-only, Commit statuses read and write, Metadata read-only, Pull requests read-only.
- **Organization permissions**: Members read-only.
- **Account permissions**: Email addresses read-only (sign-in).
- **Subscribe to events**: Push, Pull request. GitHub sends `installation` and `installation_repositories` to every App without a checkbox.

## Connecting a team

Only a team owner connects, and only as a signed-in account with a linked GitHub identity.

1. The dashboard calls `POST /api/v1/team/github-app/connect {redirect_uri}` and gets `{install_url, authorize_url, state, verifier}`. It keeps `state` and `verifier` in a short-lived `__Host-` cookie and sends the browser to `install_url`.
2. GitHub returns the browser to the setup URL with `installation_id`, `setup_action` and `state`. The dashboard checks `state` against its cookie, keeps `installation_id` in the cookie, and sends the browser to `authorize_url`.
3. GitHub returns the browser to the callback with `code` and `state`. The dashboard checks `state` again and calls `POST /api/v1/team/github-app/connect/complete {state, verifier, code, installation_id, redirect_uri}`.

The controller binds the installation to the caller's active team only when all of these hold:

- `state` carries the controller's signature, has not expired (ten minutes), names the caller's account and active team, and was not used before;
- `verifier` hashes to the value `state` carries, which only the browser that started the flow holds;
- the code redeems, with that verifier, for a GitHub user whose id equals the GitHub identity linked to the caller's account;
- GitHub's record of the installation, read with the App's own credential, names an account that user administers: the user's own account, or an organization where the user's membership is `admin`.

The last rule is stricter than "the user can see the installation" on purpose. An organization member with read access to one repository sees the organization's installation in `GET /user/installations`, and a binding would give their team source tokens for every repository in it.

An installation belongs to one team. Connecting one bound to another team answers 409 without naming the team. Reconnecting one the team already holds refreshes it.

An installation stops being bound when GitHub reports it deleted, when a team owner calls `DELETE /api/v1/team/github-app/installations/{installation_id}`, or when the operator calls `DELETE /api/v1/github-app/installations/{installation_id}`, which is how a binding moves to another team. Unbinding does not uninstall the App from GitHub.

## Runs from pushes and pull requests

A team subscribes a pipeline to a repository with `PUT /api/v1/team/github-app/triggers {repository, pipeline, push, pull_request, fork_pull_requests}`. The repository must be in one of the team's installations when the subscription is written. The pipeline is named explicitly, the way `POST /webhooks/github/{pipeline}` names it in its URL: the controller does not read a repository's `on:` block.

`POST /webhooks/github-app` verifies `X-Hub-Signature-256` with the App's webhook secret and answers 401 for a signature that does not verify. It routes by the payload's `installation.id` to the bound team; a delivery for an unbound installation is acknowledged and does nothing. For `push` and for `pull_request` (`opened`, `synchronize`, `reopened`) it creates one trigger in that team per subscribed pipeline, recording the branch, commit, repository and the installation id. Each trigger's replay key is a digest of the pipeline and the signed body, and its delivery key is the delivery id and pipeline, so a redelivery answers with the run it already started.

`installation` deliveries keep the binding current: `deleted` unbinds, `suspend` and `unsuspend` mark it. The repositories an installation covers are read from GitHub when they matter, not stored, so `installation_repositories` needs no action: adding or removing a repository on GitHub takes effect on the next token.

### Pull requests from forks

A pull request whose head repository differs from its base repository runs code the team did not write. It is ignored unless the subscription sets `fork_pull_requests`. When it runs, the run is untrusted:

- secret reads for it answer 403;
- it gets no cache grant, so it cannot write the team's shared cache;
- a metered (cloud) runner and a GitHub Actions runner credential never claim it, so it runs only on the team's own machines;
- a child run or retry of it is untrusted too.

The limit is the runner. Fork code runs on the machine that claimed it, with that machine's runner token in reach, and that token can claim the team's other work. Enable `fork_pull_requests` only for runners that isolate what they run.

## Source for cloud runners

`POST /api/v1/runs/{id}/source-token` answers a runner that holds a live claim on the run with `{token, expires_at, repository}`. The controller finds the installation GitHub reports for the run's repository, requires it to be bound to the run's team and not suspended, and mints an installation token restricted to that one repository with `contents: read`. It answers 404 when the repository has no bound installation, and 403 when the caller holds no claim on the run. The token lives at most an hour, as GitHub issues it.

A runner started with `--github-app-source` asks for one before a direct fetch of a GitHub repository and passes it to git as an `http.https://github.com/.extraheader` in the fetch's environment. It is never part of the URL, the command line or a log line, and the checkout's own git commands never see it. A runner without the flag, such as a laptop, fetches with its own credentials as before.

## Commit statuses

A run the App started reports `pending`, `success`, `failure` or `error` on its commit under the context `sparkwing/<pipeline>`, posted with an installation token restricted to that repository and `statuses: write`. Only runs whose trigger the App webhook created report this way. The operator's `GITHUB_TOKEN` reporter keeps serving operator webhook bindings, unchanged.

## GitHub Actions runners

A [GitHub Actions runner](github-actions-runners.md) credential does not create triggers. With the App installed, the push that starts the workflow also reaches the App webhook, which creates the trigger for that branch and commit, and the job's credential claims exactly that trigger. A credential that could create its own trigger would add a second way to start a run in the team, proven only by a binding the owner typed, when the installation already proves more.
