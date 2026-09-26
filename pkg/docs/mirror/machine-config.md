# Machine settings: config.yaml

Every sparkwing setting that belongs to one machine rather than one project
lives in one file, `config.yaml`, in the sparkwing config directory:
`$XDG_CONFIG_HOME/sparkwing/config.yaml`, else
`~/.config/sparkwing/config.yaml`. `SPARKWING_CONFIG` names a different file.
`SPARKWING_HOME` does not move it: the home is the state, cache and logs root,
and a profile or a registered checkout outlives any one home.

Project settings live in `.sparkwing/sparkwing.yaml` instead; see
[Config reference](config-reference.md).

## Sections

Each top-level key is a section with one owner:

| Section | Holds | Written by |
|---|---|---|
| `profiles` | named connections to controllers and storage backends, keyed by profile name | `sparkwing cloud connect`, `sparkwing configure profiles` |
| `repos` | the repo registry: `repos` (registered checkouts) and `fallback_paths` | `sparkwing run` (auto-registration), `sparkwing configure xrepo` |
| `admission` | the admission policy (`mode`, `custom`, `jev`) and the machine `budget` | you |
| `agent` | what `sparkwing-runner agent` runs: controller, token, capacity | `sparkwing cluster runners add` |
| `fleet` | the foreground fleet coordinator's listener and trusted `executors` | `sparkwing fleet init` |

Any other top-level key, and any key a section does not define, fails the read
with the file, line and key named.

```yaml
# ~/.config/sparkwing/config.yaml
profiles:
  prod:
    controller:
      url: https://api.sparkwing.example
      token: swu_...
repos:
  repos:
    - path: /home/me/code/app
  fallback_paths:
    - ~/code
admission:
  mode: auto
  # host sensor over-reads external load on this box
  budget: 50%,ignore-external
agent:
  controller: https://api.sparkwing.example
  token: swr_...
  max_concurrent: 2
  holder_prefix: desk
  contribution: 50%,50%
  local_admission: true
fleet:
  listen: 127.0.0.1:4346
  public_url: https://desk.tailnet.example
```

The sections are documented where their behavior is:
[Backends](backends.md) for profiles, [Hooks](hooks.md) for the registry,
[Local admission policy](admission.md) and
[Local execution](local-execution.md#where-to-set-it) for admission and the
budget, [Self-hosting](self-hosting.md) for the agent, and
[Local execution](local-execution.md) for the fleet.

## Permissions and writes

The file carries credentials, so sparkwing reads it only as an owner-only
regular file (mode `0600` on Unix) and refuses a symlink. A command that writes
settings rewrites only its own section: it takes a lock beside the file
(`config.yaml.lock`), reads the file, replaces that section, and renames a
complete owner-only copy over the original. Other sections and the comments
outside the rewritten section survive, and two commands writing different
sections at once both land. Comments inside the rewritten section do not
survive the rewrite.

A command running under a `SPARKWING_HOME` of its own refuses to write the
machine's `config.yaml`; point `SPARKWING_CONFIG` at a file inside that home to
keep the write there.

## Files that stay separate

- `secrets.key` is the key local secrets are sealed under. The secrets
  themselves live in `state.db` in `SPARKWING_HOME`; see
  [Secrets](cli-secrets.md) and [Local secrets](#local-secrets).
- `version-hold` records a held CLI version; see [Version](cli-version.md).

## Local secrets

`sparkwing secrets` without `--profile`, local runs, and `sparkwing web` share
one secret store: the `secrets` table of `state.db`, which the sparkwing daemon
serves on its API socket. Every value is sealed with the controller's cipher
(XChaCha20-Poly1305, bound to the row's name, scope and flags) under a 32-byte
key:

- `SPARKWING_SECRETS_KEY`, base64 of the 32 bytes, when set, as
  `sparkwing-controller` takes it;
- otherwise the key file, `secrets.key` in the config directory, or the path
  `SPARKWING_SECRETS_KEY_FILE` names. It holds the 32 raw bytes, owner-only.

The first stored secret creates the key file. Sparkwing refuses to create one
while `state.db` already holds sealed values, because a second key would leave
rows only the first one opens; restore the old file or set
`SPARKWING_SECRETS_KEY` to it. Set these variables in the environment the
daemon starts in, then run `sparkwing daemon restart`; the daemon clears them
from its own environment once read. `SPARKWING_SECRETS_PREVIOUS_KEY` keeps a
retired key readable until `sparkwing secrets rotate` reseals every value under
the current one. Back up `secrets.key` with `state.db`: neither opens the other
alone. A command running under a `SPARKWING_HOME` of its own refuses to create
the machine's key file; point `SPARKWING_SECRETS_KEY_FILE` at a file inside
that home.

A run the daemon hosts reads its secrets through the daemon. A run with no
daemon reads `state.db` directly and opens values with the same key. A fleet
worker reads its own machine's store, so a worker that keeps none has no local
secrets.

A local run reads every local secret, including an unscoped one that is not
shared, because the daemon answers this machine's own account as its
administrator. `sparkwing secrets set` stores a local secret without
`--pipeline` as shared, so this gap changes nothing for secrets set that way.

### Moving from secrets.env and config.env

The local secret store used to be two dotenv files in the config directory:
`secrets.env` for masked values and `config.env` for `--plain` ones. The daemon
imports them the first time it opens `state.db`, and `sparkwing secrets` and
local runs ask it to whenever the files exist. Each name becomes an unscoped,
shared row; a name in both files imports once, masked, with the `config.env`
value that runs used. A name `state.db` already holds keeps the store's value,
and the import names it. Each file's import is recorded with a hash of its
content, so a secret deleted after the import stays deleted, and a file edited
afterwards imports only its new names. The files are left in place for an
older sparkwing still on the machine; the notice
`imported N secrets from <file>; <file> is no longer read and can be deleted`
prints once per file.

The import reads only the default paths. `SPARKWING_SECRETS` and
`SPARKWING_CONFIG_ENV` no longer name a file, and while either is set the
import is skipped with a warning; add those values with `sparkwing secrets set`.
A command running under a `SPARKWING_HOME` of its own does not import the
machine's files. This automatic import will be removed in a later release.

## Moving from the per-file settings

Before `config.yaml`, each section was its own file in the config directory.
Sparkwing moves any it finds into `config.yaml` automatically, the first time a
command reads or writes settings, and keeps each original as
`<name>.migrated`. This automatic move will be removed in a later release; the
migration guide (`sparkwing docs search --query config.yaml`) shows the manual
move.

| Old file | Section of config.yaml |
|---|---|
| `admission.yaml` | `admission` (same keys) |
| `budget` | `admission.budget` (the file's one setting line) |
| `agent.yaml` | `agent` |
| `fleet.yaml` | `fleet` |
| `profiles.yaml` | `profiles` (the file's `profiles:` map) |
| `repos.yaml` | `repos` (same keys) |

The move checks each file with its section's own validation first and leaves a
file that fails in place, naming the error. When `config.yaml` already has a
different value in that section, the move changes nothing and names both, so
you choose which one stays. `SPARKWING_PROFILES`, `SPARKWING_REPOS` and
`SPARKWING_FLEET_CONFIG` no longer move anything; a command refuses to start
while one is set. `sparkwing doctor` lists any old file or variable still in
place.
