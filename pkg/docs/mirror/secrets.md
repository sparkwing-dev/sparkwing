# Secrets and run identity

A pipeline secret belongs to one repository and one pipeline:
`sparkwing secrets set --secret NAME --file <path> --repo OWNER/NAME --pipeline
PIPELINE`. `--repo` and `--pipeline` always go together; `--pipeline` without
`--repo` is refused. A team-wide secret is set with `--shared`. A run reads a
secret only when its plan declares it and the secret's scope admits the run. The
commands are in [cli-secrets](cli-secrets.md); declaration and the preflight
that refuses a plan missing a secret are in [pipelines](pipelines.md).

For a local run with a missing declared secret, the error names the secret and
gives its repair command, such as `sparkwing secrets set --secret BAR --file <path>`.
The run finishes as failed before any jobs start, and its status keeps the repair advice.
Run the command against the same local controller, then start the pipeline again.

## Which runs read which secrets

Pipeline secrets match GitHub repository secrets: one repository's pipeline
reads them, and a pipeline of the same name in another repository of the team
never does. Runs whose code Sparkwing fetched from the repository get its
pipeline secrets; uploaded and local runs get shared secrets only. Sparkwing
fetches the code itself, through the team's GitHub App installation, for App
pushes and pull requests, schedule fires and run-now, and dashboard or CLI runs
of a connected repository at a branch or tag head; a GitHub Actions job proves its
repository with GitHub's token. A run whose code or repository the submitter
supplies (an uploaded working tree or bundle, a local checkout, `--runner here`,
or a controller without the GitHub App) reads only `--shared` secrets, like
GitHub, where a local run never receives repository secrets.

A historical commit or locked schedule can also prove its repository when
Sparkwing selects its code through the App and GitHub's compare API confirms
that a branch or tag contains that commit. A fork-only or PR-only commit does
not pass that check. Uploaded code and changed-revision children remain asserted.
Fetching proves the repository for secrets and OIDC claims. Only a resolved
branch or tag head adds `ref` and `sha` to a manual run's OIDC token. These runs
keep their manual trust class for reserved pools, and their cache records stay
in their own fetched scope.

Personal machines and here hosts never claim repository-proven runs, including
run-now and burst `machines` work. Use a shared runner or Sparkwing Cloud for
those runs. A host's classification is sealed at claim time and is also checked
when secrets or OIDC tokens are released.

`--shared` secrets. Fetching proves the repository
for secrets and OIDC claims: a run-now or dashboard run's token claims the
repository and the branch or tag head it ran, with `trigger:manual`. It keeps its
manual trust class for reserved pools, and its cache records stay in its own
fetched scope.

`--shared` secrets are team-wide, like a GitHub organization secret with no
repository allow-list: every pipeline of every repository in the team reads
them. Protect deploy secrets with a [reserved pool](scheduling.md#reserved-pools)
or `--only-on`, which only verified runs receive. Replacing a secret's value
without `--only-on` keeps its pool.

`--repo` takes a GitHub `owner/name` or a repository key such as `gh:12345`
from `sparkwing secrets list -o json`. The controller resolves it to the
repository's GitHub ID, so a repository keeps its secrets when it is renamed.

A pipeline secret is released to a run of any branch or tag head of its
repository, the commit an App delivery names, or a historical commit the intake compare check proves, so
everyone who can push a branch or start a run of a connected repository can
read its pipeline secrets.

| Run | Pipeline secrets of |
| --- | --- |
| App push or pull request, GitHub Actions job | The pipeline in the GitHub repository it proved |
| Schedule fire or run-now whose code the App proves | The schedule's repository and pipeline |
| Dashboard, API or CLI run whose code the controller fetches through the App | The pipeline in that repository |
| Uploaded, working-tree, local or `--runner here` run | None; it reads team-wide shared secrets only |
| Child run at its parent's exact revision | The parent's repository and pipeline |
| Any other child run | None; it reads team-wide shared secrets only |
| Retry | The same repository and pipeline as the run it retries |

An asserted child reads no pipeline secret, like a GitHub reusable workflow
called without `secrets: inherit`.

## `source:` branch pins are demoted but keep pipeline secrets

A `source:` pin that names a branch or tag runs code that whoever can push that
ref chooses. Such a run is demoted to asserted: its OIDC token carries
`repository_id` but no `ref` or `sha`, it receives no reserved pool, and its cache
records stay in its own scope, so it never writes cache entries that builds of
`main` read. Sparkwing still fetches its code from the repository, so it reads
the repository's pipeline secrets.

Protect every branch a `source:` pin names as strictly as the default branch, or
pin a full commit ID, which keeps the run's identity. See
[OIDC](oidc.md#what-each-value-proves) and
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
