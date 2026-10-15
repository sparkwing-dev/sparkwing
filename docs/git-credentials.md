# Team git credentials

The shared source contract acquires an authorized checkout at the sealed SHA.
The controller declares source-release and credential-management routes. Native
source acquisition and transactional release still need integration evidence.
The sections below describe their target behavior.

<!-- design:begin -->
## Which credential a run gets

`POST /api/v1/runs/{run}/source-credential` requires a live claim with
`source.read` and the source binding. The sealed repository must be authorized
for that executor. A typed repository name or advertised runner label grants
no fetch authority.

A GitHub App credential is restricted to the authorized repository and approved
extra repositories. Releasing a stored team credential requires a same-team cloud
executor or explicit consent from the machine's owner. Team-admin token
management alone cannot provide that owner's consent. SSH credentials also
require confirmation of the pinned host key.

The store rechecks the live claim, repository permission and consent in the
transaction that records the release. A failed audit write releases nothing.
Credentials stay out of the audit record. See [credentials](credentials.md) and
[GitHub App](github-app.md#source-for-cloud-runners).

## Extra repositories

An owner manages the repository's allow-list through
`PUT /api/v1/team/github-app/extra-repos`. Members read it through
`GET /api/v1/team/github-app/extra-repos`. Source code cannot widen the list
used to issue its own credential. Every listed private repository still needs
live authorization when source is acquired.

## Store a credential

| Operation | Route |
| --- | --- |
| List metadata | `GET /api/v1/team/git-credentials` |
| Store or replace | `POST /api/v1/team/git-credentials` |
| Confirm an SSH host | `POST /api/v1/team/git-credentials/{host}/confirm` |
| Delete | `DELETE /api/v1/team/git-credentials/{host}` |
| Inspect release audits | `GET /api/v1/team/git-credentials/releases` |

Listing requires run-read permission. Writes and audit reads require team-admin
permission. Machine-owner consent uses
`PUT /api/v1/tokens/{token}/git-credentials`. It is separate from permission to
manage the team's stored credential.

Prefer a read-only deploy key or repository-scoped token. Values are sealed
under the controller's secrets key. Management reads return metadata.

## How the runner uses it

The source owner supplies credentials in memory or through a private temporary
file. The Git process receives the filtered Git environment and source-owned
argv/configuration. It cannot inherit a capability, ambient credential helper,
SSH command override or arbitrary Git configuration from the runner's shell.
Selected CA and proxy settings follow the shared process policy.

Acquisition-owned files are removed after the fetch. Credentials stay out of
clone URLs, execution seals and pipeline environments. Diagnostics use the shared
masker. A local owner credential is usable only under the runner owner's
repository allow-list.

Pipeline code running as the same OS account can reach that account's credentials
while they are in use. Repository scoping and a read-only credential limit the
exposed authority.

## Rotate and revoke

Replacing or deleting a credential changes later releases. The source-release
audit survives rotation. Controller revocation prevents new releases but cannot
recall an upstream credential that has already been issued.

`POST /api/v1/secrets/rotate` uses the encrypted-secret rotation contract. Keep
the preceding key until every encrypted value has been resealed successfully.
<!-- design:end -->
