# Team git credentials

A runner without the operator's git cache fetches each run's source straight
from its host. It holds no git credential of its own, so the controller
releases one for each run, at fetch time, to the runner holding the run's
claim.

## Which credential a run gets

`POST /api/v1/runs/{id}/git-credential` resolves the credential in a fixed
order, the same for every run:

1. **The team's GitHub App token**, when the run's repository is on github.com
   and an installation bound to the run's team covers it. The token reads only
   that repository, with `contents: read`, for at most an hour. See
   [GitHub App](github-app.md#source-for-cloud-runners).
2. Otherwise **a git credential the team stored** for the host of the run's
   repository.
3. Otherwise the route answers 404 with `{"error": "no_source_credential"}`
   and a message naming both remedies, and the run fails before anything is
   fetched.

A runner never falls back to credentials of the machine it runs on. The one
exception is a runner its owner fenced with `--allow-repo`, such as a laptop:
when the controller releases nothing, it fetches with the machine's own git
config and credentials, and only from the repositories its list names. See
[local-execution.md](local-execution.md#what-a-laptop-runner-trusts).

## Extra repositories

A run's checkout may need more of the team's private GitHub repositories,
such as submodules. By default a token reads only the run's repository,
because a token for every repository of the installation would let a
compromised pipeline read all of them.

A team owner lists the further repositories per source repository, under
**Team > GitHub > Extra repositories** in the dashboard or with
`PUT /api/v1/team/github-app/extra-repos {repository, extra_repos}`. At most
10, as `owner/name`, all of the source repository's owner, and each covered
by the installation that covers the source repository when the list is set.
An empty list clears it. Members read the lists with
`GET /api/v1/team/github-app/extra-repos`. The list lives in the controller
rather than in the repository, because code in the repository, including a
pull request's, must not widen what its own token reads.

When the run's repository has a list, the controller mints the run's App
token for it and the listed repositories together, checking again that the
installation covering the run's repository still covers each one; a listed
repository it no longer covers fails the request with 403 until an owner
updates the list. The answer names the listed repositories in
`extra_repositories`. When it names any and the checkout has a `.gitmodules`,
the runner runs `git submodule update --init --recursive --depth 1` with the
same credential. `url.insteadOf` rewrites a submodule's ssh URL on the
credential's host to https, so the one token serves every submodule. When no
installation covers the run, the team's stored github.com credential serves
the submodules the same way, rewritten to its transport, and the answer
still names the listed repositories. A run of another host has no list, and
a runner fetching with the machine's own credentials leaves submodules out.

## Store a credential

A team owner stores one credential per host, under **Team > Git
credentials** in the dashboard or with
`POST /api/v1/team/git-credentials`. Only an owner stores, replaces,
confirms or deletes one, and no route returns its value: the list shows the
host, the kind, the pinned host key and who stored it. The controller seals
the value under its secrets key, bound to the team and the host, so a
controller started without `SPARKWING_SECRETS_KEY` holds none.

There are two kinds:

- **An SSH deploy key** (`kind: ssh`, `private_key`), unencrypted, since a
  runner cannot type a passphrase. When it is stored, the controller opens an
  ssh handshake to the host (port 22 unless `port` says otherwise), reads the
  host key the way `ssh-keyscan` does, and pins it. The answer shows the key's
  SHA256 fingerprint, and the credential is unusable until an owner confirms
  that fingerprint with `POST /api/v1/team/git-credentials/{host}/confirm`.
  Check it against the fingerprint your forge publishes. The controller
  refuses a host that resolves inside its own network, and it dials the very
  address it checked, so a name that resolves differently a second time
  cannot steer the scan inward.
- **An HTTPS token** (`kind: https`, `token`, optional `username`), such as a
  GitLab or Bitbucket access token or a fine-grained GitHub token. The
  username defaults to `x-access-token`; Bitbucket repository access tokens
  need `x-token-auth`. It is usable once stored.

Prefer a read-only deploy key or token scoped to the repositories the team
builds. For a repository on github.com, connect the [GitHub App](github-app.md)
instead: it covers the repository with a token that reads that repository
alone and lives an hour, and it wins over a stored github.com credential
whenever it covers the run's repository.

A team stores at most 50 credentials.

## Who receives it

The controller releases a stored credential only when all of these hold:

- the caller holds a live claim on a node or the trigger of the run, with a
  runner token of the run's own team, both when the request arrives and
  inside the transaction that records the release, which locks the claim, so
  a lease that lapses in between releases nothing;
- the host of the run's repository is the credential's host;
- the caller is a cloud runner, one whose token the operator meters, or a
  machine a team owner opted in with
  `PUT /api/v1/team/runner-tokens/{prefix}/git-credentials` (the dashboard's
  **Team > Machines** page).

Otherwise the route answers 403 `git_credential_not_released`, or 409
`git_credential_unconfirmed` for an ssh key whose host key nobody confirmed.
A runner its owner fenced with `--allow-repo` then fetches with the machine's
own credentials; any other runner fails the run with the message.

Each release writes an audit row naming the host, the run, the runner and the
time. An owner reads the latest 200 at
`GET /api/v1/team/git-credentials/releases`. A claim asks at most ten times a
minute; past that the route answers 429.

## How the runner uses it

The runner hands the credential to the one `git fetch` that needs it and to
nothing else:

- An SSH key goes to a fresh private directory on a tmpfs (`/dev/shm`, or
  `$XDG_RUNTIME_DIR` or the temporary directory when one of those is a tmpfs)
  as a mode 0600 file, beside a `known_hosts` file holding only the pinned
  entry. A cloud runner, one its owner did not fence with `--allow-repo`,
  refuses to write the key anywhere else and fails the fetch naming the
  missing tmpfs; a fenced runner without one uses the temporary directory. The fetch runs with
  `GIT_SSH_COMMAND="ssh -i <key> -F /dev/null -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=<pinned> ..."`,
  so no other identity, agent, ssh config or known host takes part, and a
  changed host key fails the fetch. An https remote is fetched in its ssh form.
- An HTTPS token reaches git on an inherited pipe, through a credential helper
  scoped to the host, as the App token does. An ssh remote is fetched in its
  https form.
- The fetch reads none of the machine's git config and runs without its ssh
  agent.
- The runner deletes the key directory when the checkout returns, before it
  compiles or runs anything the fetched tree names. A key it cannot delete
  fails the run. A runner that crashes mid-fetch leaves the directory behind;
  the next runner of the same user to start removes every `sparkwing-git-*`
  directory in those places that it owns and no live fetch holds.
- The credential is never in an environment variable, a command line, the
  URL or `.git/config`, and the runner replaces it, and each line of a key,
  with `***` in any error text it reports.

The team's own pipeline code runs as the same user and could read the key
while the fetch runs, for example from a malicious commit in the team's own
repository. That is the team's own key, which is why a read-only one is the
recommendation.

## Rotate and revoke

Storing a credential for the same host replaces it; the next release hands
out the new value. Replacing an SSH key keeps its confirmation only while the
host's key is the one already confirmed. Deleting it
(`DELETE /api/v1/team/git-credentials/{host}`) makes every later release
fail. Nothing the runner held outlives the fetch it was released for.

`POST /api/v1/secrets/rotate` reseals every team's git credentials with the
secrets under the new key.
