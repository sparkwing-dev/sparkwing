# Secrets and run identity

A team secret is scoped to one pipeline with `sparkwing secrets set --secret
NAME --file <path> --pipeline PIPELINE`, or shared team-wide with `--shared`. A
run reads a secret only when its plan declares it and the secret's scope admits
the run. The commands are in [cli-secrets](cli-secrets.md); declaration and the
preflight that refuses a plan missing a secret are in
[pipelines](pipelines.md).

## Pipeline-scoped secrets follow the pipeline name

A pipeline-scoped secret is released to every run of that pipeline name in the
team, whatever branch or commit the run executes. This matches GitHub repository
secrets, which every workflow run of the repository can read. Restrict a secret
further with a reserved pool, which only verified runs receive; see
[reserved pools](scheduling.md#reserved-pools).

| Run | Pipeline-scoped secrets of |
| --- | --- |
| Any run started directly: App delivery, schedule, CLI, API, dashboard | Its own pipeline |
| Child run at its parent's exact revision | The parent's pipeline |
| Any other child run | None; it reads team-wide shared secrets only |
| Retry | The same pipeline as the run it retries |

An asserted child reads no pipeline-scoped secret, like a GitHub reusable
workflow called without `secrets: inherit`.

## `source:` branch pins still receive the pipeline's secrets

A `source:` pin that names a branch or tag runs code that whoever can push that
ref chooses. Such a run is demoted to asserted: its OIDC token carries no
`repository_id`, `ref` or `sha`, it receives no reserved pool, and its cache
records stay in its own scope, so it never writes cache entries that builds of
`main` read. It still receives its pipeline's scoped secrets, because those
follow the pipeline name.

Protect every branch a `source:` pin names as strictly as the default branch, or
pin a full commit ID, which keeps the run's identity. See
[OIDC](oidc.md#what-each-value-proves) and
[caching](caching.md#who-shares-a-cache).

## Values that can be secrets

Secrets of any length are accepted, encrypted and access-controlled like other
secrets. Values shorter than four Unicode characters cannot be masked in logs,
and the host derives no masking fragments from them. This includes local
secrets, secret arguments, defaults and each nonempty secret list item.
The CLI prints one warning line after a short secret is saved. The console
shows the warning once when the save succeeds. Warnings never include the value.
`POST /api/v1/secrets` returns HTTP 200 with secret metadata and an optional
`warning`; the value stays private. Secrets containing `***` are accepted.
Masking is provided by the Sparkwing host. Pipeline code run on its own is unmasked.
