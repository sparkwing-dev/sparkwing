## The saved default profile is removed

**Before:** `sparkwing profiles set --profile NAME --default` saved
`default: true` on a profile in the machine config, and every command with an
optional `--profile` sent its request to that controller when the flag was
omitted.

**After:** a command reaches a controller only when `--profile NAME` is on that
invocation. Without it, or with `--profile=`, the command uses the local
controller. `profiles set --default` is gone, `profiles list` and `profiles show`
no longer report `default`, and a config file that still carries
`default: true` fails to load with `profiles.NAME.default: unknown key`.

To migrate:

1. Delete the `default:` line from each profile in the machine config, the
   file the load error names.
2. Add `--profile NAME` to scripts, aliases and shell habits that relied on the
   default, for example `sparkwing runs list --profile prod`.
3. Reinstall git hooks and schedules that should reach a controller with an
   explicit `--profile`; ones installed without it act on this machine.

`SPARKWING_PROFILE` never selected a connection and still does not.
