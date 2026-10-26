# Secrets and run identity

A pipeline secret belongs to one repository and one pipeline:
`sparkwing secrets set --secret NAME --file <path> --repo OWNER/NAME --pipeline
PIPELINE`. `--repo` and `--pipeline` always go together; `--pipeline` without
`--repo` is refused. A team-wide secret is set with `--shared`. A run reads a
secret only when its plan declares it and the secret's scope admits the run. The
commands are in [cli-secrets](cli-secrets.md); declaration and the preflight
that refuses a plan missing a secret are in [pipelines](pipelines.md).

## Which runs read which secrets

Pipeline secrets match GitHub repository secrets: one repository's pipeline
reads them, and a pipeline of the same name in another repository of the team
never does. They are released only to runs whose repository GitHub proved: a
GitHub App push or pull request, a GitHub Actions job, or a schedule a team
admin armed. A CLI, API, dashboard, member-token, local or `--runner here` run
names its repository itself, so it reads only `--shared` secrets, like GitHub,
where a local run never receives repository secrets. `--shared` secrets are team-wide, like a GitHub organization secret
with no repository allow-list: every pipeline of every repository in the team
reads them. Protect deploy secrets with a [reserved pool](scheduling.md#reserved-pools)
or `--only-on`, which only verified runs receive.

`--repo` takes a GitHub `owner/name` or a repository key such as `gh:12345`
from `sparkwing secrets list -o json`. The controller resolves it to the
repository's GitHub ID, so a repository keeps its secrets when it is renamed.

A pipeline secret is released whatever branch or commit the run executes.

| Run | Pipeline secrets of |
| --- | --- |
| App delivery, GitHub Actions push, admin-armed schedule | The pipeline in the GitHub repository it proved |
| CLI, API, dashboard, member-token, local or `here` run | None; it reads team-wide shared secrets only |
| Child run at its parent's exact revision | The parent's repository and pipeline |
| Any other child run | None; it reads team-wide shared secrets only |
| Schedule a non-admin armed, overrode or locked | None; it reads team-wide shared secrets only |
| Retry | The same repository and pipeline as the run it retries |

An asserted child reads no pipeline secret, like a GitHub reusable workflow
called without `secrets: inherit`.

## `source:` branch pins read only shared secrets

A `source:` pin that names a branch or tag runs code that whoever can push that
ref chooses. Such a run is demoted to asserted: its OIDC token carries no
`repository_id`, `ref` or `sha`, it receives no reserved pool, its cache records
stay in its own scope, so it never writes cache entries that builds of `main`
read, and it reads only `--shared` secrets.

Pin a full commit ID to keep the run's identity and its repository's pipeline
secrets. See [OIDC](oidc.md#what-each-value-proves) and
[caching](caching.md#who-shares-a-cache).

## Values that can be secrets

Secrets of any length are accepted, encrypted and access-controlled like other
secrets. Values shorter than four Unicode characters cannot be masked in logs,
and the host derives no masking fragments from them. This includes local
secrets, secret arguments, defaults and each nonempty secret list item.
Derived proxy credential parts must meet the same length to be masked.
The CLI prints one warning line after a short secret is saved. The console
shows the warning once when the save succeeds. Warnings never include the value.
`POST /api/v1/secrets` returns HTTP 200 with secret metadata and an optional
`warning`; the value stays private. Secrets containing `***` are accepted.
Masking is provided by the Sparkwing host. Pipeline code run on its own is unmasked.
