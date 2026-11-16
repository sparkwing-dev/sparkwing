# Known limitations

These are the constraints a team moving from GitHub Actions is most likely to hit. Each entry gives the workaround. [Migrate from GitHub Actions](migrate-from-github-actions.md) maps the rest of a
workflow.

## Your machine

### Linux needs a systemd user session

On a systemd host, local runs start their runner through your user manager. On WSL without a session bus, in an SSH
shell without lingering, or in a container, a run cannot start a runner. Run `loginctl enable-linger $USER` once, or
start from a login session; see [running with no daemon](local-execution.md#running-with-no-daemon-available).

### Windows is experimental

Native Windows binaries ship, but the runner service is not installed, schedules are recorded without firing, and
local runs can fail at controller startup. Use WSL2 with systemd and the Linux installer. See
[Windows with Git Bash](local-execution.md#windows-with-git-bash).

### Local jobs share your working tree

A run on your machine works in your checkout, not in a fresh clone per job. Build outputs stay in it after the run,
jobs that run at the same time can write the same paths, and a job sees files another job wrote whether or not it
declared `Consumes`. Write each job's outputs under its own directory, such as `dist/<target>`, so one job's
`Artifacts` glob does not pick up another's files. Declare `Consumes` anyway: on Cloud and shared runners each job has
its own workspace and receives only what it consumes. **By design for local runs.**

### Services and containers need Docker

Service containers and Docker helpers need a Docker daemon on the machine that runs the job. Sparkwing Cloud
provides one per job. **By design.**

## Triggers

### `on:` in `sparkwing.yaml` acts only through the GitHub App

`on.push` and `on.pull_request` start runs only after a team owner enables repository automation in
**Team → GitHub**; see [repository-managed triggers](github-app.md#repository-managed-triggers). Locally, `on:` is
ignored: `sparkwing run` runs any pipeline you name. **By design.**

### Pull requests from forks do not run

A fork's code would run with the runner's credentials in reach, so the GitHub App ignores fork pull requests. Push
the branch to the main repository, or have a maintainer run it with `sparkwing run`.

### Path filters turn a pipeline off

Repository automation skips any pipeline that declares `on.push.paths`, and the preview lists it under `skipped`.
Drop `paths` and skip jobs in code instead: compare `changedFiles` from the Git helpers against your paths in
`SkipIf`.

### Tag and release triggers need a manual subscription

Repository YAML reads only `on.push.branches` and `on.pull_request`. Subscribe a release pipeline to tags or releases
under **Team → GitHub → Advanced** ([manual subscriptions](github-app.md#manual-subscriptions)).

### Superseded runs are not cancelled

A new push to a branch or pull request does not cancel its older run, so both run to completion. Cancel the older one
with `sparkwing runs cancel`, and keep pull request pipelines short.

## Secrets and deployments

### No deployment environments

There is no environment object with required reviewers. A repository's pipeline secrets reach a run of any branch of
that repository, and `--shared` secrets reach every team member who can start a run, so treat them as visible to
every editor. Put deploy credentials behind `sparkwing secrets set --only-on <pool>`, and serve that pool with a
`--launcher` runner enrolled with `--trust verified-default`; see [reserved pools](scheduling.md#reserved-pools).

### Approvals are a workflow step, not a security control

An approval pauses a run until someone answers, and the run's own author can approve it. A retry of only the failed
jobs re-runs an approval-gated job without asking again. Protect deploys with reserved pools as above, not with
approvals.

### Schedule arguments and argument defaults are stored unsealed

A schedule's `args` and the defaults of job arguments declared with `WithArgs` are stored in the controller's database
without encryption. Team members cannot read them, but a database or backup reader can. Pass credentials only as
secrets.

## Runners

### Personal machines never run push or pull request work

A runner enrolled with `--personal` takes only its owner's manual runs. It never takes push, pull request, release or
schedule runs, or any run whose code Sparkwing fetched from the repository. Use Sparkwing Cloud or a shared runner
for CI. **By design.**

### Enroll a shared runner only under a dedicated user

A shared runner runs any pushed branch of its repositories as the OS user it runs under, with that user's SSH keys,
cloud credentials and browser sessions in reach. Use `--personal` on a daily-driver account. Enroll a shared runner
only under a dedicated OS user or VM with its own `HOME` and `SPARKWING_HOME`, scoped with `--repos` to one repository
or to repositories that trust each other:

```bash
sparkwing runners add --runner build-box --repos github.com/acme/app --profile cloud
```

See [how runners advertise labels](scheduling.md#how-runners-advertise-labels).

## Toolchains and Docker

### Cloud runners ship only Go and Node

Cloud runners have Go 1.26, Node 22, Docker, Git, make, jq and curl, with no version choice and no other languages.
Your own machines and runners use whatever they have installed, and there is no `setup-*` step. Fetch the version you
need into a cached directory, as in
[setup and toolchains](migrate-from-github-actions.md#checkout-setup-and-toolchains), or run it with `docker run`.

### Docker layer cache does not carry across Cloud jobs

Each Cloud job gets a fresh Docker daemon, so image builds start cold. Use a registry cache with BuildKit's
`--cache-to` and `--cache-from`; see [fast builds](fast-builds.md).

## TypeScript

### TypeScript pipelines on Cloud and shared runners

Remote TypeScript execution is implemented but has had far less use than Go. Try your pipeline on the runners you
will use before you depend on them, and report what breaks.

### TypeScript reference

Most [SDK](sdk.md) pages show Go. The [migration guide](migrate-from-github-actions.md) shows every common feature in
both languages, and the SDK's `index.d.ts` in `.sparkwing/node_modules/@sparkwing/sdk` lists the full API.

### `pipeline lint` does not check TypeScript

`sparkwing pipeline lint` reports TypeScript pipelines as unsupported. Run `tsc --noEmit` and
`sparkwing pipeline plan` instead.

### No TypeScript sparks-core blocks

The [sparks-core](sparks-core.md) deploy and release blocks are Go modules. In TypeScript, call the same tools from
shell steps or npm packages.

### No detached child pipelines in TypeScript

`ctx.runPipeline` starts a child and waits for it. Go can also start a child and await it later. Use awaited children
in TypeScript.

## Reporting

### One check run per pipeline

Each GitHub App run posts one `sparkwing/<pipeline>` check with counts and a link. Job names, errors, summaries and
annotations stay in the dashboard, and runs started from the CLI post no check. To require separate checks, split the
work into separate pipelines.

### No failure email

Sparkwing sends no email when a run fails. Subscribe a receiver to [run events](run-events.md), such as the Slack
example there.

### No artifact list in the dashboard

List and download artifacts with `sparkwing runs artifacts`. Artifacts are kept for 30 days, with no per-artifact
retention.

## Stability

### The API is pre-1.0

A minor release can change the SDK, CLI or configuration. Each breaking change ships with a migration guide. See
[versioning](versioning.md). **By design until 1.0.**
