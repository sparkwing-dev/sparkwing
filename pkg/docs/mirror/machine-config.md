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
    - path: /srv/code/app
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
complete owner-only copy over the original. Other sections and their comments
survive, and two commands writing different sections at once both land. Within
the rewritten section, a key that survives keeps its comments; a removed key
takes its comments with it. A write that would remove an anchor an alias in
another section uses is refused, naming the anchor.

A command running under a `SPARKWING_HOME` of its own refuses to write the
machine's `config.yaml`; point `SPARKWING_CONFIG` at a file inside that home to
keep the write there.

## Files that stay separate

- `secrets.key` is the key local secrets are sealed under. The secrets
  themselves live in `state.db` in `SPARKWING_HOME`; see
  [Secrets](cli-secrets.md) and [Local secrets](#local-secrets).
- `version-hold` records a held CLI version; see [Version](cli-version.md).

## Local secrets

`sparkwing secrets` without `--profile`, local runs, and the dashboard (`sparkwing serve`) share
one secret store: the `secrets` table of `state.db`, which the sparkwing daemon
serves on its API socket. Every value is sealed with the controller's cipher
(XChaCha20-Poly1305, bound to the row's name, scope and flags) under a 32-byte
key:

- `SPARKWING_SECRETS_KEY`, base64 of the 32 bytes, when set, as
  `sparkwing-controller` takes it;
- otherwise the key file, `secrets.key` in the config directory, or the path
  `SPARKWING_SECRETS_KEY_FILE` names. It holds the 32 raw bytes, owner-only.

The daemon creates the key file on the first stored secret; no other process
creates it, so a daemon started with `SPARKWING_SECRETS_KEY` and a
the dashboard (`sparkwing serve`) started without it cannot end up with two keys. The dashboard
and a run refuse to seal until the key exists; store the first secret with
`sparkwing secrets set`. Sparkwing reads the key file only as an
owner-only regular file that is not a symlink. It refuses to create one
while `state.db` already holds sealed values, because a second key would leave
rows only the first one opens; restore the old file or set
`SPARKWING_SECRETS_KEY` to it. Set these variables in the environment the
daemon starts in, then run `sparkwing daemon restart`; like any variable,
they reach every process started from that environment, pipeline steps
included. `SPARKWING_SECRETS_PREVIOUS_KEY` keeps a
retired key readable until `sparkwing secrets rotate` reseals every value under
the current one. Back up `secrets.key` with `state.db`: neither opens the other
alone. A command running under a `SPARKWING_HOME` of its own refuses to create
the machine's key file; point `SPARKWING_SECRETS_KEY_FILE` at a file inside
that home.

The key keeps a copied or backed-up `state.db` sealed. It does not keep
secrets from code running as your account: a pipeline step can read the key
file, and the daemon answers every process of your account.

A run the daemon hosts reads its secrets through the daemon. A run with no
daemon reads `state.db` directly and opens values with the same key. A fleet
worker reads its own machine's store, so a worker that keeps none has no local
secrets.

A local run reads every local secret, including an unscoped one that is not
shared, because the daemon answers this machine's own account as its
administrator. `sparkwing secrets set` stores a local secret without
`--pipeline` as shared, so this gap changes nothing for secrets set that way.

### Moving from secrets.env and config.env

The local secret store used to be two dotenv files: `secrets.env` for masked
values and `config.env` for `--plain` ones, in the config directory or where
`SPARKWING_SECRETS` and `SPARKWING_CONFIG_ENV` pointed. The daemon imports the
files its own environment names, once, when it first opens `state.db`. Each
name becomes an unscoped, shared row; a name in both files imports once,
masked, with the `config.env` value that runs used. A name `state.db` already
holds keeps the store's value. The import parses both files before it writes
anything, then commits the rows and a record of the import in one
transaction. Once recorded it never runs again, so a secret deleted afterwards
stays deleted and later edits to the files are ignored. The daemon log names
each imported name and each name that kept the store's value. The files are
left in place for an older sparkwing still on the machine; delete them once
none needs them.

An import that fails, on a malformed line for example, imports nothing. Until
it succeeds, `sparkwing secrets` and every secret a local run reads fail with
its error; fix the file and run `sparkwing daemon restart`. A command running
under a `SPARKWING_HOME` of its own does not import the machine's files. This
automatic import will be removed in a later release.

## Moving from the per-file settings

Before `config.yaml`, each section was its own file in the config directory.
Sparkwing copies any it finds into `config.yaml` automatically, the first time a
command reads or writes settings, and leaves each original in place so older
sparkwing binaries on the machine keep reading it. Once `config.yaml` has the
section, the original is ignored; delete it when no older binary needs it. This
automatic copy will be removed in a later release; the migration guide
(`sparkwing docs search --query config.yaml`) shows the manual move.

| Old file | Section of config.yaml |
|---|---|
| `admission.yaml` | `admission` (same keys) |
| `budget` | `admission.budget` (the file's one setting line) |
| `agent.yaml` | `agent` |
| `fleet.yaml` | `fleet` |
| `profiles.yaml` | `profiles` (the file's `profiles:` map) |
| `repos.yaml` | `repos` (same keys) |

The copy checks each file with its section's own validation first. A file that
fails is not copied, and only reads of that section fail, naming the file and
the key to remove. The copy goes only into the machine's own `config.yaml`,
never into a file `SPARKWING_CONFIG` names, and is skipped with a warning
under a sandboxed `SPARKWING_HOME`. `SPARKWING_PROFILES`, `SPARKWING_REPOS`
and `SPARKWING_FLEET_CONFIG` no longer move anything; a command refuses to
start while one is set. `sparkwing doctor` lists old files already copied,
which are safe to delete, apart from any it could not copy.
