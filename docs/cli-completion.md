<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing completion

Every `sparkwing completion` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing completion`

Emit a shell completion script (bash|zsh|fish)

Prints a completion script for the selected shell. Source it
from your shell rc:

  \# bash
  source <(sparkwing completion --shell bash --output plain)

  \# zsh (add 'autoload -U compinit; compinit' once above)
  source <(sparkwing completion --shell zsh --output plain)

  \# fish
  sparkwing completion --shell fish --output plain | source

zsh and fish get per-item descriptions; bash is name-only because
compgen lacks the facility.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (pretty on a terminal, json when piped) |
| `--shell NAME` | bash \| zsh \| fish (required) |

### Examples

```sh
# Wire completion for the current zsh session
source <(sparkwing completion --shell zsh --output plain)

# Install persistent completion for fish
sparkwing completion --shell fish --output plain > ~/.config/fish/completions/sparkwing.fish
```
