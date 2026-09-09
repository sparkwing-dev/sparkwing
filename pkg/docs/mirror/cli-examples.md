<!-- GENERATED from the CLI command registry by `sparkwing commands --format markdown --output plain`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing examples

Every `sparkwing examples` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing examples`

Read complete example pipelines

Read complete pipelines from the sparks-core example registry. Examples
cover container deployment, migrations, release publishing, test sharding,
and similar tasks.

--category and --cloud filter the list. Cloud-independent examples match
every cloud filter. --name reads one example's description, prerequisites,
parameters, applicability, and README. --body includes source with default
parameter values.

JSON output contains manifests for a list, or a manifest and README for one
example. Use 'sparkwing pipeline new --template <shape>' to start a pipeline.

### Flags

| Flag | Description |
|---|---|
| `--name EXAMPLE` | Show full detail for one example instead of the list |
| `--body` | With --name, print the pipeline source (default + <placeholder> params) |
| `--category CATEGORY` | Filter the list by applicability category |
| `--cloud CLOUD` | Filter the list by cloud (aws \| gcp); cloud-agnostic examples always match |
| `-o, --output FORMAT` | Output format: pretty \| json \| plain (default: pretty on TTY, json when piped) |

### Examples

```sh
# Browse them
sparkwing examples

# Read one
sparkwing examples --name container-deploy-ecs-fargate --body

# Search deployment guidance
sparkwing docs search -q "ecs fargate"
```
