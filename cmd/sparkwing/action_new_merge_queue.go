package main

const mergeQueueScript = `set -euo pipefail
remote=origin
fail() { printf '%s\n' "$*" >&2; exit 1; }
clean() { test -z "$(git status --porcelain)" || fail 'Checkout changed; inspect it and submit again.'; }
test -n "$SUBMITTED" || fail 'Submit from a committed work branch.'
clean
test "$(git rev-parse HEAD)" = "$SUBMITTED" || fail 'HEAD changed since submission; submit again.'
branch=$(git symbolic-ref --short HEAD) || fail 'Use a work branch.'
base=$(git ls-remote --symref "$remote" HEAD | sed -n 's@^ref: refs/heads/\([^[:space:]]*\)[[:space:]]HEAD$@\1@p')
test -n "$base" || fail 'Cannot discover the remote default branch.'
test "$branch" != "$base" || fail 'Use a work branch, not the default branch.'
git fetch "$remote" "refs/heads/$base"
if ! git merge --no-edit FETCH_HEAD; then
  git merge --abort
  fail 'Merge conflict; resolve it in this worktree and submit again.'
fi
clean
head=$(git rev-parse HEAD)
printf 'Intent: %s\n' "$BRIEF"
bash bin/check.sh
clean
test "$(git symbolic-ref --short HEAD)" = "$branch" || fail 'Branch changed during checks.'
test "$(git rev-parse HEAD)" = "$head" || fail 'HEAD changed during checks.'
git push "$remote" "$head:refs/heads/$base"
printf 'Landed %s\n' "$head"
`

const mergeQueueTemplate = `package jobs

import (
	"context"
	"errors"
	"strings"

	sw "github.com/sparkwing-dev/sparkwing/sparkwing"
)

type {{STRUCT}} struct{ sw.Base }

type {{STRUCT}}Args struct {
	Brief string ` + "`flag:\"brief\" required:\"true\" desc:\"Intent for this landing\"`" + `
}

func ({{STRUCT}}) ShortHelp() string { return {{SHORTLIT}} }

func ({{STRUCT}}) Plan(_ context.Context, plan *sw.Plan, in {{STRUCT}}Args, run sw.RunContext) error {
	if strings.TrimSpace(in.Brief) == "" {
		return errors.New("--brief requires the intent for this landing")
	}
	if run.Git == nil || run.Git.SHA == "" {
		return errors.New("submit from a committed work branch")
	}
	plan.Concurrency(sw.NewConcurrencyGroup("merge-queue", sw.ConcurrencyLimit{Capacity: 1}))
	sw.Job(plan, "land", &{{STRUCT}}Job{Dir: sw.WorkDir(), Submitted: run.Git.SHA, Brief: in.Brief}).Inline()
	return nil
}

type {{STRUCT}}Job struct {
	sw.Base
	Dir, Submitted, Brief string
}

func (j *{{STRUCT}}Job) Work(w *sw.Work) (*sw.WorkStep, error) {
	sw.Step(w, "land", j.land).Risk("destructive", "prod")
	return nil, nil
}

func (j *{{STRUCT}}Job) land(ctx context.Context) error {
	_, err := sw.Bash(ctx, {{QUEUE_SCRIPT}}).Dir(j.Dir).Env("SUBMITTED", j.Submitted).Env("BRIEF", j.Brief).Run()
	return err
}

func init() {
	sw.RegisterEntrypoint[{{STRUCT}}Args]("{{STRUCT}}", func() sw.Pipeline[{{STRUCT}}Args] { return &{{STRUCT}}{} })
}
`

const mergeQueueTypeScriptTemplate = `import { ConcurrencyGroup, definePipeline, workDir } from "@sparkwing/sdk";

definePipeline({
  name: "{{STRUCT}}",
  short: {{SHORTLIT}},
  args: [{ name: "brief", type: "string", required: true, description: "Intent for this landing" }],
  plan(p, run) {
    const brief = run.args.brief ?? "";
    if (!brief.trim()) throw new Error("--brief requires the intent for this landing");
    const submitted = run.git.commit;
    if (!submitted) throw new Error("submit from a committed work branch");
    const cwd = workDir();
    p.concurrency(new ConcurrencyGroup("merge-queue", { capacity: 1 }));
    const job = p.job("land").inline();
    job.step("land", async (ctx) => {
      await ctx.bash({{QUEUE_SCRIPT}}, { cwd, env: { SUBMITTED: submitted, BRIEF: brief } }).run();
    }).risk("destructive", "prod");
  },
});
`
