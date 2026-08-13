<!-- GENERATED from the CLI command registry by `sparkwing commands -o markdown`. Do not edit by hand; regenerate with `bash bin/gen-cli-docs.sh`. -->
<!-- markdownlint-disable MD004 MD007 MD030 MD032 -->
# CLI reference: sparkwing examples

Every `sparkwing examples` command, flag, and argument, generated from the CLI's own command registry. All command groups are indexed in [cli-reference.md](cli-reference.md).

## `sparkwing examples`

Worked pipelines to read, not starting points to scaffold

The sparks-core registry: complete, working pipelines --
container deploys for AWS and GCP, migrations, canary rollouts,
release publishing, test sharding. The template-verify pipeline
proves every one compiles, lints, and explains, and runs the
runnable-tier ones, so unlike prose they cannot quietly stop
being true.

Read them, do not scaffold from them. 'sparkwing pipeline new
--template <shape>' starts a pipeline; an example shows how a real
one is built once you have the shape. Reach for one when you want
to know how something is done rather than to begin.

Most arrivals should come through 'sparkwing docs search', which
ranks examples alongside the docs -- searching "ecs fargate"
answers the question without anyone browsing a list.

--category and --cloud narrow the list; a cloud-agnostic example
always passes a --cloud filter. --name switches to a full detail
view: description, when-to-use, prerequisite, parameters,
applicability, and README. Add --body for the pipeline source
rendered with each parameter's default.

-o json emits manifests for the list, or manifest + README
(+ rendered body with --body) for one example.

### Flags

| Flag | Description |
|---|---|
| `--name EXAMPLE` | Show full detail for one example instead of the list |
| `--body` | With --name, print the pipeline source (default + <placeholder> params) |
| `--category CATEGORY` | Filter the list by applicability category |
| `--cloud CLOUD` | Filter the list by cloud (aws \| gcp); cloud-agnostic examples always match |
| `-o, --output FORMAT` | Output format: pretty \| json (default: pretty) |

### Examples

```sh
# Browse them
sparkwing examples

# Read one
sparkwing examples --name container-deploy-ecs-fargate --body

# Usually you want this instead
sparkwing docs search -q "ecs fargate"
```
