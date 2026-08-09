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
sparkwing pipeline hooks status      # report declared, installed, and missing hooks
sparkwing pipeline hooks survey      # report which registered repos git gates at all
sparkwing pipeline hooks uninstall   # remove sparkwing-managed hooks only
```

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

Managed hooks render quietly by default: each run prints one progress
line and a one-line pass/fail status with the run id, instead of
streaming every step into the commit or push. On failure the hook
surfaces the failing step's error; the full log stays retrievable with
`sparkwing runs logs --run <id>`. The hook sets
`SPARKWING_LOG_FORMAT=quiet`; export a different value (`pretty` or
`json`) before the git command to see the full stream.

### Hooks are per repository, and the repository says so

The rule, so a new checkout does not have to rediscover it: **every repository
that declares a gate claims `core.hooksPath` for its own hook directory, and
chains the machine's global hooks from there.** A machine-wide hooks directory
is never where a gate lives.

That is not a preference. `core.hooksPath` has no search path -- one directory
wins outright -- so a global directory holding the gates would have to dispatch
on which repository git happened to be in, and every repository's gate would
change whenever that one directory did, with no commit anywhere to show it. The
repository that a gate protects is the only place that can declare it, review
it, and version it.

The global directory keeps whatever the machine put there. It is reached
through a forwarder in each repository's own hook directory, so `prepare-commit-msg`
and friends still fire, and a repository is never asked to choose between its
gate and the machine's hooks.

Two failures follow from breaking the rule, and `hooks survey` names both:
a repository whose hooks are **shadowed** by the global path holds a full set of
gates and runs none, and one whose `core.hooksPath` points at a **sibling
repository** runs that repository's gates instead of its own.

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
without publishing dormant candidate hooks. Clear it with
`git -C <repo> config --unset core.hooksPath` and re-run
`sparkwing pipeline hooks install --repo <repo>`.

Claiming `core.hooksPath` is also what would stop a global hook firing if
nothing in `.git/hooks` hands off to it, so install refuses the claim while
any global hook name is unforwarded -- a hand-written hook of that name is
the usual reason, since install never overwrites one. It names the hook and
leaves the machine's hooks in charge without publishing candidates; remove
that file and re-run to get both layers. Once the claim is already in place
there is no such decision to make, so install and `pipeline hooks status`
both report a global hook nothing forwards to, however it came about.

`sparkwing pipeline hooks status` and `sparkwing doctor` both report a hook
directory git is not reading, naming the gates that stopped firing and how
to restore them.

### Which repositories are gated at all

One repository at a time is how a repository gets forgotten. `hooks survey`
answers for every checkout in the local registry at once:

```bash
sparkwing pipeline hooks survey            # every registered repo, classified
sparkwing pipeline hooks survey --ungated  # just the ones accepting ungated commits
sparkwing pipeline hooks install --fleet   # arm all of them
```

Each repository gets one state:

| State | What it means |
| --- | --- |
| `armed` | git runs the repository's own gate for every hook it declares |
| `shadowed` | gates are installed and `core.hooksPath` sends git elsewhere, so none fire |
| `uninstalled` | a declared hook was never written |
| `borrowed` | git runs a gate for a declared hook out of another repository's hook directory |
| `undeclared` | no pipeline asks for a hook, so there is nothing to arm |

Each declared hook lands in exactly one of `firing`, `borrowed`, `shadowed` and
`missing`, so a hook is never reported as both installed and missing. The
one-word state takes the worst of them, and `borrowed` is the worst: a shadowed
or uninstalled repository is honest about accepting ungated commits, while a
borrowed gate refuses them under a state word that used to read as no gate at
all.

`borrowed` counts as ungated even though commits really are refused. Nothing in
the repository declares the file that runs, so an uninstall in the repository
that owns it disarms this one with no commit here and no warning, and the rules
being enforced are the other repository's. Fixing it means clearing this
repository's own override first -- install treats a repository-scoped
`core.hooksPath` as deliberate and will not touch it:

```bash
git -C <repo> config --unset core.hooksPath
sparkwing pipeline hooks install --repo <repo>
```

`sparkwing doctor` reports the ungated ones too, including on a run that finds
nothing else to repair, and states the clean verdict when there are none -- a
silent report is indistinguishable from one produced by a build too old to
survey a fleet at all.

A survey that cannot read the registry says so and exits non-zero. It never
answers with an empty fleet, because that is what a machine with nothing
registered answers, and one stray character in `repos.yaml` used to make an
unread fleet look like a swept one. `install --fleet` and `fire --fleet` refuse
for the same reason. `doctor` keeps reporting the rest of its sweep and carries
the reason in `gates_survey_error`, since the registry says nothing about this
home's runs, locks or daemon.

### What a hook directory cannot tell you

Everything above reads files. That is enough to find a gate nobody installed,
and not enough to establish one that fires: a shadowed repository inspects as
fully installed and refuses nothing, and a borrowed one inspects as installing
nothing and refuses everything. Only a commit settles it.

```bash
sparkwing pipeline hooks fire            # this repo: make the gate refuse a commit
sparkwing pipeline hooks fire --fleet    # every registered repo
```

`fire` stages a file and commits it with the gate told to refuse, then reports
whether git refused it and which hook file did. Everything happens in a
throwaway linked worktree, which shares the repository's config -- so the same
`core.hooksPath` and the same hooks apply -- while carrying its own index and
its own detached HEAD, so the repository's working tree, index, branches and
HEAD are untouched whatever the gate does.

`fire` settles the pre-commit gate only. A pre-push gate needs a remote to fire
against, so it is left to the push that exercises it, and a repository that
declares no pre-commit trigger has nothing for `fire` to prove -- `--fleet`
counts it apart from the repositories whose gate refused rather than among
them.

Every refusal is checked against a control: the same staged change is committed
again with hooks switched off, and has to land. Without that, a commit that
failed for an unrelated reason reads exactly like a gate doing its job.

Only a hook sparkwing wrote that carries the self-test guard is ever executed.
A hand-written hook, or a managed one installed before the guard existed, is
reported `unprovable` and `fire` exits non-zero -- an enforcement question that
could not be answered is not a pass. Re-run `hooks install` to replace an older
managed hook with one that can be asked.

The guard is the `SPARKWING_HOOK_SELFTEST` environment variable, and it can only
make a gate refuse. There is no value that lets a commit through, because a
variable that skipped a gate would be a bypass with an environment variable for
a key.

Ungated means a hook that can refuse work does not fire. Only `pre-commit` and
`pre-push` can, so those are the hooks `--ungated`, `doctor` and the `--fleet`
summary count: a repository whose only missing hook is `post-commit` loses a
notification rather than a gate, and one that declares no blocking hook has no
gate to arm -- `--fleet` counts it apart from the repositories it armed rather
than among them, because a sweep that reports it as armed reports a gated
fleet while every commit in that repository still goes unchecked.

The list is the machine's repo registry -- `~/.config/sparkwing/repos.yaml`,
which `sparkwing configure xrepo add <dir>` writes to and which can name
`fallback_paths` directories to scan for `*/.sparkwing/`. That is the whole
extent of the survey: a checkout the registry does not reach is not surveyed,
not swept by `--fleet`, and not reported by `doctor`. Register it, or add the
directory it lives under to `fallback_paths`, before reading a clean survey as
a clean machine.

### Arming a repository whose gate cannot run

An ungated repository still accepts work; a repository whose gate is armed but
cannot execute rejects every commit. The second is worse, and installing a
hook is what converts the first into the second.

So before those gates can fire, install runs the repository's blocking gates
once. A gate that does not pass -- a red pipeline, an admission daemon the
repository's pinned SDK cannot speak to -- rejects the whole installation
before any candidate hook filename or config value is published. Existing
hooks remain callable throughout the proof. Once every proof passes, each
complete hook replaces its predecessor with an atomic rename and only then may
the repository's `core.hooksPath` change. A later write or config error restores
every managed hook, global-hook forwarder, file mode, and repository
`core.hooksPath` value exactly. A failed reinstall therefore cannot remove a
previously working commit or push gate, and a first install cannot leave only
the gates whose proofs happened to pass. The failure is printed with the
command to re-run. `--no-prove` skips the proof for an operator who has already
made it.

The proof runs on every install that leaves a gate live, not only the one that
claims `core.hooksPath`. An install rewrites every hook the repository
declares, so a repository git already reads needs the same proof as a newly
armed one. Proof-before-publication keeps its prior gates live while that proof
runs or if it fails.

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
