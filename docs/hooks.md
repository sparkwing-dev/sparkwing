# Triggers

Pipelines fire from three places:

1. **Webhooks** -- the controller matches an incoming GitHub event
   against `on:` blocks in `.sparkwing/pipelines.yaml`.
2. **Manual / API invocation** -- `wing <pipeline>`, `sparkwing run
   <pipeline>`, `sparkwing run <pipeline> --on prof` for remote
   dispatch.
3. **Git hooks** (optional) -- `sparkwing pipeline hooks install` writes
   pre-commit / pre-push hook files into `.git/hooks/` that fan out to
   pipelines declaring `pre_commit:` / `pre_push:` triggers. Hooks are
   opt-in and managed: install / uninstall / status are explicit verbs,
   and unmanaged hooks the user wrote by hand are left alone.

## Webhook triggers

```yaml
# .sparkwing/pipelines.yaml
build-deploy:
  description: Build and deploy on push to main
  on:
    push:
      branches: [main]
      paths: ["*.go", "go.mod"]      # optional path filter
  tags: [ci, deploy]
```

| Trigger | When |
|---------|------|
| `push` | After push to remote (webhook -> controller) |
| `pull_request` | On PR events |
| `schedule` | Cron schedule |
| `pre_commit` | Local git pre-commit hook (after `hooks install`) |
| `pre_push` | Local git pre-push hook (after `hooks install`) |

See [api.md](api.md) for `POST /webhooks/github/{pipeline}` and HMAC
verification.

## Manual / API invocation

```bash
wing build-deploy                                       # local
wing build-deploy --on prod                             # remote dispatch
sparkwing run build-deploy --on prod                    # canonical form
sparkwing pipeline run --pipeline build-deploy --on prod  # explicit form
```

`sparkwing runs triggers list --on prod` surfaces queued / claimed /
done triggers on the controller; `sparkwing runs triggers get --id ...`
inspects one. To fire a fresh trigger (the sparkwing equivalent of
`gh workflow run`), invoke the pipeline directly with `--on PROF`.

## Git hooks

Git hooks are opt-in. After declaring `pre_commit:` or `pre_push:` on
a pipeline in `pipelines.yaml`, install them once per checkout:

```bash
sparkwing pipeline hooks install     # writes .git/hooks/pre-commit, pre-push
sparkwing pipeline hooks status      # report which sparkwing hooks are installed
sparkwing pipeline hooks uninstall   # remove sparkwing-managed hooks only
```

Each managed hook carries a marker comment so `uninstall` and `status`
can distinguish sparkwing-installed hooks from hand-written ones.
Existing unmanaged hooks are skipped on install with a warning.

## Running checks locally without a hook

If you don't want hooks managing your git lifecycle, just run the
pipeline:

```go
// .sparkwing/jobs/lint.go
import sw "github.com/sparkwing-dev/sparkwing/sparkwing"

type Lint struct{ sw.Base }

func (p *Lint) Plan(_ context.Context, plan *sw.Plan, _ sw.NoInputs, rc sw.RunContext) error {
    sw.Job(plan, rc.Pipeline, sw.JobFn(func(ctx context.Context) error {
        if err := sw.Bash(ctx, "gofmt -l .").MustBeEmpty("formatting drift"); err != nil {
            return err
        }
        _, err := sw.Bash(ctx, "go vet ./...").Run()
        return err
    }))
    return nil
}
```

```bash
wing lint        # runs locally; no git hook required
```
