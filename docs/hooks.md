# Triggers

Pipelines fire from several sources:

1. **GitHub App** -- a GitHub event runs the pipelines a team subscribed
   to the repository through the [GitHub App](github-app.md).
2. **Manual / API invocation** -- `sparkwing run <pipeline>` for local
   execution, `sparkwing pipeline trigger <pipeline> --profile prof` for
   remote dispatch.
3. **Git hooks** (optional) -- `sparkwing pipeline hooks install` writes
   pre-commit / pre-push / post-commit hook files into `.git/hooks/` that
   fan out to pipelines declaring `pre_commit:` / `pre_push:` /
   `post_commit:` triggers. Hooks are opt-in and managed: install /
   uninstall / status are explicit verbs, and unmanaged hooks the user
   wrote by hand are left alone.

## GitHub triggers

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: build-deploy
    entrypoint: BuildDeploy
    description: Build and deploy on push to main
    on:
      push:
        branches: [main]
        paths: ["*.go", "go.mod"]      # optional path filter
```

The trigger keys that go under `on:` -- and their fields -- are listed
in the generated [config-reference.md](config-reference.md); this page
covers how each fires.

GitHub events reach the controller through the [GitHub App](github-app.md):
a team owner connects the App's installation and subscribes each pipeline to
a repository, choosing its events and branch filters there. The controller
does not read your `sparkwing.yaml`, so the `branches` / `paths` / `actions`
filters under `on:` are declarative: they document intent, but the
controller does not gate on them. The subscription's own `branches` and
`base_branches` filters are what the controller applies.

When the policy concerns the branch Sparkwing checks out, enforce it at
run start with a pipeline guard. The guard blocks before any step runs:

```yaml
pipelines:
  - name: deploy
    entrypoint: Deploy
    on:
      push:
        branches: [main] # documents the intended GitHub event
    guards:
      when: [git:branch=main]
```

The literal name must match the checked-out head. For pull requests that is
the head branch, not the base named by `pull_request.branches`.
`git:branch=default` matches only when the dispatch supplies default-branch
metadata, which trigger claims do not always supply, so use an explicit
name. Branch guards do not implement path
filters, custom pull-request action filters, or pull-request base-branch
matching. Enforce those policies at the event source or in pipeline code;
do not treat the `on:` fields as authorization controls.

### Connecting a repository

In the dashboard, a team owner opens **Team -> GitHub**, connects the App's
installation, and adds a subscription naming the repository, the pipeline and
its events. The same subscription is
`PUT /api/v1/team/github-app/triggers`:

```bash
curl -sS -X PUT https://<controller>/api/v1/team/github-app/triggers \
  -H "Authorization: Bearer $SPARKWING_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"repository":"your-org/my-app","pipeline":"build-deploy","push":true,"pull_request":true,"branches":["main"]}'
```

[GitHub App](github-app.md) lists every event and filter a subscription takes.

## Pull request triggers

```yaml
# .sparkwing/sparkwing.yaml
pipelines:
  - name: pr-gate
    entrypoint: PRGate
    description: Lint and test every pull request
    on:
      pull_request:
        branches: [main]               # declarative: PRs targeting main
```

A subscription with `pull_request` fires on the `opened`, `synchronize`, and
`reopened` actions -- the ones that change the diff. A pull request whose
head repository is not its base repository, or whose head repository was
deleted, starts nothing: the controller acknowledges it as ignored, because
a fork's code would run with the team's runner token and secrets in reach. Other actions
(`labeled`, `closed`, `edited`, ...) start nothing unless the subscription
selects them, so a gate does not re-run every time someone relabels the PR. A
configured `actions` field under `on:` records intent and does not change
what the subscription selects.

The run checks out the **PR head** commit. The pipeline reads the PR
context from `RunContext.Trigger.PullRequest`:

```go
type PRGate struct{ sw.Base }

func (p *PRGate) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    if pr := rc.Trigger.PullRequest; pr != nil {
        sw.Job(plan, "diff-base", func(ctx context.Context) error {
            _, err := sw.Bash(ctx, "git diff --stat origin/"+pr.BaseRef).Run()
            return err
        })
    }
    return nil
}
```

**Status reporting.** Each run the App started reports as one check run
named `sparkwing/<pipeline>` on the commit it builds; see
[GitHub App](github-app.md#check-runs). Set the controller's `--dashboard-url` to the
public dashboard base URL to make each check run link to `/runs?run=<run-id>`.

## Manual / API invocation

```bash
sparkwing run build-deploy                                  # local execution
sparkwing pipeline trigger build-deploy --profile prod      # remote dispatch
```

`sparkwing runs list --profile prod` lists controller runs.
`sparkwing runs status --run RUN --profile prod` reads one run's safe display
projection, including its jobs and attempts. To fire a fresh trigger (the sparkwing equivalent of
`gh workflow run`), use `sparkwing pipeline trigger <pipeline> --profile PROF`.

## Git hooks

Git hooks are opt-in. Declare the trigger on a pipeline in
`.sparkwing/sparkwing.yaml`, giving it a body even when it takes no options:

```yaml
pipelines:
  - name: fast-checks
    entrypoint: FastChecks
    on:
      pre_commit: {}
```

Then install the hooks once per checkout:

```bash
sparkwing pipeline hooks install     # writes .git/hooks/pre-commit, pre-push, post-commit
sparkwing pipeline hooks status      # report declared, installed, and missing hooks
sparkwing pipeline hooks uninstall   # remove sparkwing-managed hooks only
```

`status` emits one `kind: hook` record per installed hook when piped, followed
by a `kind: summary` record with the repository, installed count, gate state,
and hook state. Hook records contain the name, pipelines, and whether the
installed content matches the repository configuration. `--output plain` prints only hook
names. `--output pretty` keeps the readable report and repair instructions.
If configuration loading fails, status still lists installed hooks, reports
the error on stderr, and exits nonzero. It emits no JSON summary in that case.

Hooks run their pipelines on this machine. Installation writes hooks without
running pipelines; run a gate manually with `sparkwing run <pipeline>` to check it.

Each managed hook carries a marker comment so `uninstall` and `status`
can distinguish sparkwing-installed hooks from hand-written ones.
Existing unmanaged hooks are skipped on install with a warning.
`sparkwing info` puts the repair command first in its next steps whenever a
declared hook is missing or not firing.

`pre_commit` and `pre_push` are blocking: the hook aborts the commit or
push when a pipeline fails. `post_commit` is non-blocking -- the commit
has already landed, so the hook runs its pipelines, tolerates failures,
and always exits zero. Keep post-commit pipelines fast or detach their
slow work; the hook runs in the commit's foreground.

Managed hooks use plain output by default. Runs stream their output and print
start and finish records with the run ID and status. The hook sets
`SPARKWING_LOG_FORMAT=plain`. Export `pretty` or `json` before the Git command
to select another format.

### Repository hook paths

Each repository installs hooks in its own Git hook directory. Linked worktrees
share the main checkout's hook directory.

When `core.hooksPath` points elsewhere, installation refuses before changing
hooks or Git configuration. Compose the hooks manually, or clear the override
and install again. The refusal names the active directory and the command to
clear the local or global setting. Check the setting before clearing it because
other repositories may rely on global hooks.

`status` compares each installed hook with the expected content and format and
reports missing, modified, non-executable, no longer declared, or shadowed hooks. Run it
in the affected repository, repair the reported hook or configuration, and
reinstall there. `doctor` also reports a shadowed hook in the current repository.

### Hook-launched pipelines are unbound from the repository

git tells a hook which repository it is acting on through the environment:
`GIT_INDEX_FILE` on the commit paths, `GIT_DIR`, `GIT_WORK_TREE`,
`GIT_PREFIX` and friends elsewhere. Those variables are inherited by
everything the hook starts, and they outrank a directory argument -- a step
that runs `git -C /tmp/scratch add -A` from inside a gate stages into the
commit being gated, not into the scratch repository. A partial commit
(`git commit -- path`) hands over an absolute index path, and that case
fails the commit outright.

sparkwing removes the repository-binding `GIT_*` variables from its own
environment before doing anything else, so pipelines, their steps, and the
third-party tools those call all discover a repository the way they would
from a plain shell. Nothing in a pipeline needs to opt in, and no hook needs
reinstalling for it -- the behavior lives in the binary. The one thing that
would be lost with them, the index holding the commit under test, is kept
under a name git does not act on; see below.

Managed hooks scrub them a second time, in a subshell around the pipeline
invocations, which covers the `sparkwing` on `PATH` being older than the
install that wrote the hook.

Identity (`GIT_AUTHOR_*`, `GIT_COMMITTER_*`) and config selection
(`GIT_CONFIG_GLOBAL`, `GIT_CONFIG_NOSYSTEM`, ...) are left alone: they carry
your intent rather than a repository, and test fixtures that isolate
themselves rely on them.

### Reading what is being committed

The working directory is no substitute for the index git handed the hook.
git points a commit hook at the index it is composing that commit in, and
only a commit of already-staged content points at the repository's own:

| how the commit was made | `GIT_INDEX_FILE` git exports |
|---|---|
| content staged first, then `git commit` | `.git/index` |
| `git commit -a` | `<repo>/.git/index.lock` |
| `git commit -- <path>` | `<repo>/.git/next-index-<n>.lock` |

Where git names a lock file, the repository's own index is stale: it does not
yet hold what is being committed, so `git diff --cached` read through it
reports an empty change. A staged-diff check that simply lost the binding
would pass every such commit without looking at it.

So the binding survives the unbind under a name git does not act on.
sparkwing exports `SPARKWING_GATE_INDEX`, an absolute path, before dropping
`GIT_INDEX_FILE`, and a step that wants the staged content binds it to the one
command that should see it:

```bash
GIT_INDEX_FILE="$SPARKWING_GATE_INDEX" git diff --cached --name-only
```

Bind it per command rather than exporting it: ambient, it is exactly the
binding that lets a `git add` in a scratch checkout write into the commit
being gated. Treat an empty value as "no gate is running" and fall back to the
repository's index, and check the file still exists before using it -- git
deletes that index when the commit finishes, and a git pointed at a missing
index quietly reads an empty one. sparkwing's own comment gate does this: it
reads `GIT_INDEX_FILE` when a caller set one deliberately, the gate index when
a hook started the run, and the repository's index otherwise.

## Running checks locally without a hook

If you don't want hooks managing your git lifecycle, just run the
pipeline:

```go
// .sparkwing/jobs/lint.go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"

type Lint struct{ sw.Base }

func (p *Lint) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, rc.Pipeline, func(ctx context.Context) error {
        if err := sw.Bash(ctx, "gofmt -l .").MustBeEmpty("formatting drift"); err != nil {
            return err
        }
        _, err := sw.Bash(ctx, "go vet ./...").Run()
        return err
    })
    return nil
}
```

```bash
sparkwing run lint        # runs locally; no git hook required
```

Hook installation and removal report the resulting hook status. Piped commands
emit JSON records by default. Use `-o plain` for hook names or `-o pretty` for a
readable report. Installation progress and warnings go to stderr.
