<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing profile

Every `sparkwing profile` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing profile`

Show which profile sparkwing would use right now, and why

Reports the profile a sparkwing command would resolve to and
the chain that picked it (flag > project hint > detect > default
\> builtin laptop), using the same resolver 'sparkwing run' and
'sparkwing pipeline trigger' use -- so the answer matches what
they would actually do.

With no flag it shows the active no-flag resolution. With
--profile NAME it shows the hypothetical: what adding that flag
to your next command would select. Tokens are never printed.

### Flags

| Flag | Description |
|---|---|
| `--profile NAME` | Show the hypothetical resolution for `--profile NAME` |
| `-o, --output FORMAT` | Output format: pretty\|json (default: pretty) |

### Examples

```sh
# Active profile with no flag
sparkwing profile

# What would --profile prod pick
sparkwing profile --profile prod

# Machine-readable
sparkwing profile -o json
```
