# Migrating to the next release

This guide covers breaking changes listed under `[Unreleased]`. At release,
move the sections into the versioned migration guide and update changelog links.

## Serve command

The local dashboard and API lifecycle now lives under `sparkwing serve`.
Update shell scripts, shortcuts, and runbooks to use these commands:

| Previous command | Replacement |
| --- | --- |
| `sparkwing dashboard start` | `sparkwing serve start` |
| `sparkwing dashboard status` | `sparkwing serve status` |
| `sparkwing dashboard kill` | `sparkwing serve kill` |
| `sparkwing dashboard stop` | `sparkwing serve stop` |

The retired `dashboard` command fails with a replacement instruction and
performs no service action. Existing lifecycle flags and exit codes stay the
same. The dashboard UI, HTTP routes, JSON service identity `dashboard`, and
`dashboard.pid` / `dashboard.log` state paths keep their names, so `serve`
addresses the same local service and state.

## Lint slots removed

`sparkwing.AcquireLintSlot`, the `LintSlot` type, its `Configure` and
`ConfigureIn` methods, and `SPARKWING_LINT_SLOTS` are gone. Give each worktree
its own linter cache with `ToolCacheDir` instead.

Before:

```go
slot, err := sparkwing.AcquireLintSlot("golangci-lint")
if err != nil {
    return err
}
defer slot.Release()

cmd := sparkwing.Bash(ctx, "golangci-lint run --allow-serial-runners ./...")
_, err = slot.Configure(cmd, "GOLANGCI_LINT_CACHE").Run()
```

After:

```go
_, err := sparkwing.Bash(ctx, "golangci-lint run --allow-serial-runners ./...").
    Env("GOLANGCI_LINT_CACHE", sparkwing.ToolCacheDir("golangci-lint")).
    Run()
```

A multi-module repository sets `Dir` per module and hands each invocation the
same worktree-scoped cache.

### Why the slot could not be kept

A slot lent every worktree one alias path -- a symlink repointed at whichever
worktree held the lease -- so that a shared cache's stored absolute paths kept
resolving. git resolves that alias and reports the real worktree as the
repository root, so every finding the linter recorded under the alias sat
outside the diff that a baseline such as golangci-lint's `new-from-merge-base`
matches against, and the baseline filter dropped all of them. A tree with eight
findings linted clean in three seconds. The alias breaks the filter whether or
not the cache is shared, so keying the cache by tree would not have restored
the findings.

A worktree therefore starts its linter cold, which is the cost of a gate that
reports what is in the tree.
