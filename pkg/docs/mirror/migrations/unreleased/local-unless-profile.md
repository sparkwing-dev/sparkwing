## The saved default profile and machine run route are removed

**Before:** `sparkwing profiles set --profile NAME --default` saved
`default: true` on a profile in the machine config, and every command with an
optional `--profile` sent its request to that controller when the flag was
omitted.

**After:** a command reaches a controller only when its invocation asks for one:
`--profile NAME`, or for `sparkwing run` an explicit `--route cloud` or
`--route burst`, which uses `run.routing.profile` when `--profile` is absent.
Without either, or with `--profile=`, the command uses the local controller.
`profiles set --default` is gone, `profiles list` and `profiles show` no longer
report `default`, and a config file that still carries `default: true` fails to
load with:

```text
profiles.NAME.default: line N: was removed; pass --profile NAME on each command that should reach that controller (see `sparkwing docs migrations`)
```

To migrate:

1. Delete the `default:` line from each profile in the machine config, the
   file the load error names.
2. Add `--profile NAME` to scripts, aliases and shell habits that relied on the
   default, for example `sparkwing runs list --profile prod`.
3. Reinstall schedules that should reach a controller with an explicit
   `--profile`; ones installed without it act on this machine. Managed git hooks
   run `sparkwing run` without `--route`, so they now always run locally.

`SPARKWING_PROFILE` never selected a connection and still does not.

### Machine `run.routing.route`

**Before:** `route: cloud` under `run.routing` in the machine config sent every
bare `sparkwing run` through Cloud overflow, including runs from git hooks and
scripts that never named a route.

**After:** a run leaves this machine only when the invocation asks.
`--route burst` applies the machine `run.routing` preferences, overflow
allowance and local fallback; `--route cloud` still runs directly on Cloud.
A run without `--route` is local. A config that still carries
`run.routing.route` fails to load and names this change.

To migrate, delete `route:` from `run.routing` and add `--route burst` to the
runs that should overflow, for example `sparkwing run test --route burst`.
The other `run.routing` keys (`profile`, `prefer`, `overflow_minutes`,
`local_secrets`, `source_files`) keep their meaning.
