# GitHub Actions runners

A repository's own GitHub Actions minutes can run its Sparkwing work. A workflow in the repository starts `sparkwing-runner`, which proves which repository it runs in with the job's GitHub ID token, claims only that team's nodes for that repository, and exits when the queue stays empty.

Placement order for a team's work:

1. The team's own machines, the runners that advertise the preferred local label.
2. GitHub Actions jobs, which advertise `github-actions` and take a node only after the [placement hold](scheduling.md#local-first-placement-in-claim-mode) expires or when no local runner with room is polling.

## Why a job reaches only its own repository

GitHub's terms allow Actions to be used for the production, testing, deployment or publication of the software project in the repository where the workflow runs. The controller enforces that boundary: a job's credential claims a node, a trigger, or reads a run only when the run's trigger names the job's repository in every repository field it carries (`github_owner`/`github_repo`, `repo`, `repo_url`, and `GITHUB_REPOSITORY` in its environment). The comparison is case-insensitive and accepts the `https`, `ssh` and `git@github.com:` spellings of `github.com/<owner>/<name>`.

## Two-sided consent

A binding is dangerous in either direction, so an exchange needs both sides:

- An **owner** of the team binds the repository by GitHub's numeric repository id and owner id. The ids survive renames, and a repository transferred to another owner stops matching.
- The **workflow** names the team with `--team`. A team that binds a repository it does not control gets nothing, because that repository's workflows never name it.

The workflow stores no secret. The job requests an ID token with `permissions: id-token: write` and the controller's external URL as the audience, and `POST /api/v1/runners/github/exchange` verifies it against GitHub's published keys (issuer `https://token.actions.githubusercontent.com`, RS256, audience, expiry). The controller refuses the exchange when it has no external URL.

## Set up a repository

1. Look up the ids:

   ```bash
   gh api repos/acme/widgets --jq '.id, .owner.id'
   ```

2. As a team owner, bind the repository:

   ```bash
   curl -X POST "$CONTROLLER/api/v1/team/github-runners" \
     -H "Authorization: Session $SESSION" -H 'Content-Type: application/json' \
     -d '{"repository":"acme/widgets","repository_id":123456,"repository_owner_id":7890}'
   ```

   The answer and `GET /api/v1/team/github-runners` carry the workflow with the controller URL and team filled in.

3. Commit it as `.github/workflows/sparkwing.yaml`:

```yaml
# Runs Sparkwing work for this repository on this repository's GitHub
# Actions minutes. The controller hands the job only nodes of runs for this
# repository, and the job exits once the queue has been empty for --idle-exit.
name: sparkwing
on:
  push:
  workflow_dispatch:
permissions:
  id-token: write
  contents: read
jobs:
  sparkwing:
    runs-on: ubuntu-latest
    # The runner credential lives one hour, so the job does too.
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v4
      - name: Install sparkwing-runner
        run: |
          base=https://github.com/sparkwing-dev/sparkwing/releases/latest/download
          curl -fsSLO "$base/sparkwing-runner-linux-amd64"
          curl -fsSLO "$base/SHA256SUMS"
          grep ' sparkwing-runner-linux-amd64$' SHA256SUMS | sha256sum -c -
          install -m 0755 sparkwing-runner-linux-amd64 "$RUNNER_TEMP/sparkwing-runner"
      - name: Run this repository's Sparkwing work
        run: |
          "$RUNNER_TEMP/sparkwing-runner" runner --github-actions \
            --team acme \
            --controller https://sparkwing.example.com \
            --idle-exit 2m
```

`DELETE /api/v1/team/github-runners/{repository_id}` removes a binding and revokes every live credential minted under it.

## What the credential can do

The exchange mints a runner-kind token for the team with the runner scope bundle, principal `github:<repository_id>:<owner>/<name>`, and a one-hour lifetime. A team holds at most 20 live GitHub Actions credentials; the 21st exchange answers 429. The token appears in the team's runner-token list, where an owner can revoke it.

Every request the credential makes passes a fence that lists the routes it may use:

| Route | Rule |
| --- | --- |
| `POST /api/v1/nodes/claim` | the queue scan sees only the team's nodes of runs for the repository; executor offers are refused |
| `POST /api/v1/triggers/claim` | the same filter on triggers |
| `/api/v1/runs/{id}/...`, `/api/v1/triggers/{id}/...` | the run's trigger must belong to the team and name the repository |
| `POST /api/v1/runs`, concurrency slots, pipeline profile writes, secrets | already bound to a live claim, which the rules above confine |
| anything else | 403 |

## Runner behavior

`sparkwing-runner runner --github-actions` reads `ACTIONS_ID_TOKEN_REQUEST_URL` and `ACTIONS_ID_TOKEN_REQUEST_TOKEN`, exchanges the token, adds the `github-actions` label, and claims nodes only. It stops taking new nodes ten minutes before the credential expires. `--idle-exit` ends the job once no node has been held for that long, and a node in flight keeps the job alive whatever the flag says.

A claimed node fetches its source through the controller's run-scoped Git cache, not from the job's `actions/checkout` directory.
