## Read verbs use list, show and status

**Before:** selected records used `describe`, `info` or `get`, and the job
diagnostics command was named `debug env`. Cache state used `cache info`.

**After:** selected records use `show`, and operational state uses `status`,
with the same flags as before:

| Before | After |
|---|---|
| `sparkwing pipeline describe --pipeline NAME` | `sparkwing pipeline show --pipeline NAME` |
| `sparkwing repos info -C PATH` | `sparkwing repos show -C PATH` |
| `sparkwing cache info` | `sparkwing cache status` |
| `sparkwing cache info --docs` | `sparkwing cache status --docs` |
| `sparkwing secrets get --secret NAME` | `sparkwing secrets show --secret NAME` |
| `sparkwing debug env --run ID --job JOB` | `sparkwing debug show --run ID --job JOB` |

An old spelling is refused with usage exit 2 and a hint naming the replacement.
Nothing runs. Update scripts that call the old spellings and regenerate shell
completion, for example with `sparkwing completion --shell bash -o plain`.

Output formats, selectors and exit codes keep their meaning. `secrets show`
reads unmasked variables. Masked secrets remain readable only by a pipeline
run that declares them. Pretty and plain output keep the value's exact bytes
without adding a newline.

`runs status --run ID --follow` keeps its lifecycle and outcome behavior.
`info --for-agent` keeps the orientation entry point. Documentation still uses
`docs read`, and collection commands still use `list`. The SDK's internal
`describe` protocol and pipeline pins keep their meaning.
SDK upgrade verification discovers the selected engine's detail command, so it
can verify both older and newer engines.
