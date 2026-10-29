<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing doctor

Every `sparkwing doctor` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing doctor`

Inspect local controller and machine diagnostics

Identify removed timer verbs, service version skew and incompatible store schemas through the local controller lifecycle. Inspection does not open state.db. Partial reports retain findings and exit nonzero when controller inspection fails.

### Flags

| Flag | Description |
|---|---|
| `-o, --output FORMAT` | Pretty on a terminal; JSON when piped; explicit format wins. Values: pretty, json, plain. |
| `-C, --checkout PATH` | Resolve the project from this local checkout path. |
| `-h, --help` | Show command help without executing. |
| `--help-all` | Include uncommon flags in command help. |
| `--dry-run` | Show what would change without changing anything. |
| `--timeout DURATION` | Longest time to spend inspecting the machine and local controller, 10s by default. |
