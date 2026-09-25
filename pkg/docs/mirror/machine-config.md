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
complete owner-only copy over the original. Other sections and their comments
survive, and two commands writing different sections at once both land. Within
the rewritten section, a key that survives keeps its comments; a removed key
takes its comments with it. A write that would remove an anchor an alias in
another section uses is refused, naming the anchor.

A command running under a `SPARKWING_HOME` of its own refuses to write the
machine's `config.yaml`; point `SPARKWING_CONFIG` at a file inside that home to
keep the write there.

## Files that stay separate

- `secrets.env` and `config.env` are the local secret store; see
  [Secrets](cli-secrets.md).
- `version-hold` records a held CLI version; see [Version](cli-version.md).

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
