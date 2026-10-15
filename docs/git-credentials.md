# Team git credentials

`POST /api/v1/runs/{run}/source-credential` releases the stored credential
for a live claim's sealed source location. It takes no body. The response
contains `kind`, `host`, `secret`, optional `username` and optional
`known_hosts`. The host already has the checkout URL, SHA and policy in its
sealed execution input.

The controller derives the credential's host from the sealed source location.
The store checks live ancestry, the current claim fence, repository permission,
executor enrollment and machine-owner consent in one transaction. It commits
the release audit before the controller opens the envelope. The envelope is
bound to the team and host, so copying it to another row does not open it.

A bare machine root cannot release source credentials. Trigger, plan and job
capabilities need `source.read` and the run's source binding. A job loses source
access when execution starts. Unknown, revoked or ended credentials receive
401. Live authority without the required grant or binding receives 403.
Errors use the shared `{"error":{"code","message"}}` envelope.

## Extra repositories

Team owners manage GitHub App extra-repository lists at
`PUT /api/v1/team/github-app/extra-repos`. The server stores repository IDs
with the list so a rename cannot transfer access to a reused name. Stored
source release checks the sealed source repository against current enrollment.

## Store a credential

A team owner stores one credential per host, under **Team > Git
credentials** in the dashboard or with
`POST /api/v1/team/git-credentials`. Only an owner stores, replaces,
confirms or deletes one, and no route returns its value: the list shows the
host, the kind, the pinned host key and who stored it. The controller seals
the value under its secrets key, bound to the team and the host, so a
controller started without `SPARKWING_SECRETS_KEY` holds none.

`GET /api/v1/team/git-credentials` requires live member or verified local
authority with `runs.read`. It lists the authenticated team's metadata and
uses `Cache-Control: no-store`. Query parameters and caller headers cannot
select another team. Stored secrets are omitted, including when no decryption
key is configured. Machine roots, claims and bootstraps cannot read this list.

A credential is one of these kinds:

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
builds.

A team stores at most 50 credentials.

## Who receives it

A live authorized claim receives a stored credential only when its executor
is a team Cloud runner, or its machine owner has consented. Cloud identity
comes from the award and current enrollment. Machine consent belongs to the
account that registered the machine. Team administration cannot replace it.

The machine owner changes consent with
`PUT /api/v1/tokens/{token}/git-credentials` and `{"enabled":true}` or
`{"enabled":false}`. This requires the owner's account session and CSRF proof.
The store rechecks the account and machine under the same lock used by release.

Each release records the repository, host, run, job, generation, machine,
owner, capability prefix and store timestamp. Audit failure returns no
credential. The source controller requires the bound secrets cipher before
requesting a release.

## Use the credential

Use the released credential for the sealed source location. Keep it in memory
or a private temporary file and remove that file when git exits. The SSH
response carries the confirmed known_hosts entry for the credential's host.

## Rotate and revoke

Storing a credential for the same host replaces it; the next release hands
out the new value. Replacing an SSH key keeps its confirmation only while the
host's key is the one already confirmed. Deleting it
(`DELETE /api/v1/team/git-credentials/{host}`) makes every later release
fail. Nothing the runner held outlives the fetch it was released for.

`POST /api/v1/secrets/rotate` reseals every team's git credentials with the
secrets under the new key.
