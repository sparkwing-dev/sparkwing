# Triggers

Pipelines fire from several sources:

1. **Webhooks** -- the controller matches an incoming GitHub event
   against `on:` blocks under `pipelines:` in `.sparkwing/sparkwing.yaml`.
2. **Manual / API invocation** -- `sparkwing run <pipeline>` for local
   execution, `sparkwing pipeline trigger <pipeline> --profile prof` for
   remote dispatch.
3. **Git hooks** (optional) -- `sparkwing pipeline hooks install` writes
   pre-commit / pre-push hook files into `.git/hooks/` that fan out to
   pipelines declaring `pre_commit:` / `pre_push:` triggers. Hooks are
   opt-in and managed: install / uninstall / status are explicit verbs,
   and unmanaged hooks the user wrote by hand are left alone.

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

## Manual / API invocation

```bash
sparkwing run build-deploy                                  # local execution
sparkwing run build-deploy --profile prod                   # local, state via prod
sparkwing pipeline trigger build-deploy --profile prod      # remote dispatch
```

`sparkwing runs triggers list --profile prod` surfaces queued / claimed /
done triggers on the controller; `sparkwing runs triggers get --id ...`
inspects one. To fire a fresh trigger (the sparkwing equivalent of
`gh workflow run`), use `sparkwing pipeline trigger <pipeline> --profile PROF`.

## Git hooks

Git hooks are opt-in. After declaring `pre_commit:` or `pre_push:` on
a pipeline in `.sparkwing/sparkwing.yaml`, install them once per checkout:

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
sparkwing run lint        # runs locally; no git hook required
```
