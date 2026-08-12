<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing completion

Every `sparkwing completion` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing completion`

Emit a shell completion script (bash|zsh|fish)

Prints a completion script for the selected shell. Source it
from your shell rc:

  \# bash
  source <(sparkwing completion --shell bash)

  \# zsh (add 'autoload -U compinit; compinit' once above)
  source <(sparkwing completion --shell zsh)

  \# fish
  sparkwing completion --shell fish | source

zsh and fish get per-item descriptions; bash is name-only because
compgen lacks the facility.

### Flags

| Flag | Description |
|---|---|
| `--shell NAME` | bash \| zsh \| fish (required) |

### Examples

```sh
# Wire completion for the current zsh session
source <(sparkwing completion --shell zsh)

# Install persistent completion for fish
sparkwing completion --shell fish > ~/.config/fish/completions/sparkwing.fish
```
