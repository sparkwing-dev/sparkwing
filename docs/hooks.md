# Triggers

Pipelines fire from several sources:

1. **Webhooks** -- the controller matches an incoming GitHub event
   against `on:` blocks under `pipelines:` in `.sparkwing/sparkwing.yaml`.
2. **Manual / API invocation** -- `sparkwing run <pipeline>` for local
   execution, `sparkwing pipeline trigger <pipeline> --profile prof` for
   remote dispatch.
3. **Git hooks** (optional) -- `sparkwing pipeline hooks install` writes
   pre-commit / pre-push / post-commit hook files into `.git/hooks/` that
   fan out to pipelines declaring `pre_commit:` / `pre_push:` /
   `post_commit:` triggers. Hooks are opt-in and managed: install /
   uninstall / status are explicit verbs, and unmanaged hooks the user
   wrote by hand are left alone.

## Webhook triggers

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
covers how each fires. See [api.md](api.md) for
`POST /webhooks/github/{pipeline}` and HMAC verification.

Both `push` and `pull_request` arrive on the same GitHub webhook
endpoint (`POST /webhooks/github/{pipeline}`) and fire the pipeline the
URL names. The controller does not read your `sparkwing.yaml`, so the
`branches` / `paths` / `actions` filters under `on:` are declarative:
they document intent, but the controller does not gate on them. Scope a
trigger by configuring the GitHub webhook to deliver only the events you
want to that pipeline's URL.

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

A `pull_request` trigger fires on the `opened`, `synchronize`, and
`reopened` actions -- the ones that change the diff. Other actions
(`labeled`, `closed`, `edited`, ...) are acknowledged and ignored, so a
gate does not re-run every time someone relabels the PR. This default
action set is applied by the controller; the `actions` field records
intent but is not yet enforced.

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

**Status reporting.** sparkwing does not yet report a run's result back
to the pull request as a GitHub commit status or check. A `pull_request`
run executes and shows up on the sparkwing dashboard, but GitHub's
merge-blocking required-checks UI will not see it. Gate merges on the
sparkwing side (or via a thin GitHub Action that waits on the run) until
native status reporting lands.

## Manual / API invocation

```bash
sparkwing run build-deploy                                  # local execution
sparkwing run build-deploy --profile prod                   # local, state via prod
sparkwing pipeline trigger build-deploy --profile prod      # remote dispatch
```

`sparkwing runs triggers list --profile prod` surfaces pending / claimed /
done triggers on the controller; `sparkwing runs triggers get --id ...`
inspects one. To fire a fresh trigger (the sparkwing equivalent of
`gh workflow run`), use `sparkwing pipeline trigger <pipeline> --profile PROF`.

## Git hooks

Git hooks are opt-in. After declaring `pre_commit:`, `pre_push:`, or
`post_commit:` on a pipeline in `.sparkwing/sparkwing.yaml`, install them
once per checkout:

```bash
sparkwing pipeline hooks install     # writes .git/hooks/pre-commit, pre-push, post-commit
sparkwing pipeline hooks status      # report which sparkwing hooks are installed
sparkwing pipeline hooks uninstall   # remove sparkwing-managed hooks only
```

Each managed hook carries a marker comment so `uninstall` and `status`
can distinguish sparkwing-installed hooks from hand-written ones.
Existing unmanaged hooks are skipped on install with a warning.

`pre_commit` and `pre_push` are blocking: the hook aborts the commit or
push when a pipeline fails. `post_commit` is non-blocking -- the commit
has already landed, so the hook runs its pipelines, tolerates failures,
and always exits zero. Keep post-commit pipelines fast or detach their
slow work; the hook runs in the commit's foreground.

Managed hooks render quietly by default: each run prints one progress
line and a one-line pass/fail status with the run id, instead of
streaming every step into the commit or push. On failure the hook
surfaces the failing step's error; the full log stays retrievable with
`sparkwing runs logs --run <id>`. The hook sets
`SPARKWING_LOG_FORMAT=quiet`; export a different value (`pretty` or
`json`) before the git command to see the full stream.

### When your machine sets `core.hooksPath`

A `core.hooksPath` in your global git config replaces `.git/hooks` for
every repository on the machine. Hooks written into `.git/hooks` are then
never read, and a gate disappears without a message.

`pipeline hooks install` handles this. When it finds a global
`core.hooksPath`, it sets `core.hooksPath` for this repository to the
repository's own hook directory -- a repository setting wins over the global
one -- and chains the global hooks from there, so nothing is lost:

- A hook name both layers define runs the pipelines first, then hands off
  to the global hook. A failing blocking pipeline aborts before the
  hand-off, so the commit or push still stops where you expect.
- A hook name only the global config defines gets a forwarder, so hooks
  like `prepare-commit-msg` keep firing. Only names git itself runs as
  hooks are forwarded; a helper script or note kept in that directory is
  left where it is.

Forwarders resolve the global path when they run, so changing the machine's
hooks directory does not mean reinstalling. They carry the same marker as
any other managed hook, so `uninstall` removes them -- and releases the
repository's `core.hooksPath` at the same time, putting the machine's hooks
back in charge rather than stranding them behind a claim with nothing left
to forward.

If the repository already sets its own `core.hooksPath` pointing somewhere
else, install leaves it alone -- that setting was deliberate -- and warns
that the hooks it just wrote will not run. Clear it with
`git config --unset core.hooksPath` and re-run.

Claiming `core.hooksPath` is also what would stop a global hook firing if
nothing in `.git/hooks` hands off to it, so install refuses the claim while
any global hook name is unforwarded -- a hand-written hook of that name is
the usual reason, since install never overwrites one. It names the hook and
leaves the machine's hooks in charge; remove that file and re-run to get
both layers. Once the claim is already in place there is no such decision to
make, so install and `pipeline hooks status` both report a global hook
nothing forwards to, however it came about.

`sparkwing pipeline hooks status` and `sparkwing doctor` both report a hook
directory git is not reading, naming the gates that stopped firing and how
to restore them.

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
install that wrote the hook. The subshell is also what keeps the scrub off
the hand-off to a global hook: that hook is git's to configure, so it gets
the environment git meant it to have.

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
