# Security

How sparkwing protects code, credentials, and infrastructure.

## Authentication and authorization

Controller and logs requests carry a bearer token; each route declares
the scope it needs. Tokens are typed (`swu_`/`swr_`/`sws_`), stored as
argon2id hashes, and never logged in full. The complete model -- token
kinds, the scope set, per-endpoint enforcement, the unauthenticated
endpoints, and first-visit admin bootstrap -- is in
[auth.md](auth.md). Sparkwing does not have a "root token"; the `admin`
scope is the superset.

## Webhooks

GitHub webhook deliveries are verified by the controller: it checks the
`X-Hub-Signature-256` HMAC against `GITHUB_WEBHOOK_SECRET` with a
constant-time compare before doing any work. The handler acts on `push`
and on `pull_request` (opened / synchronize / reopened, against the PR
head), and answers `ping`; other event types and other `pull_request`
actions are accepted and ignored.

When `GITHUB_TOKEN` is set, the controller uses it only for outbound
commit-status requests for `pull_request` webhook runs. Prefer a
fine-grained token limited to the served repositories with **Commit
statuses: Read and write**. The token never enters trigger environment,
run state, logs, or the dashboard. An empty token disables outbound
status reporting.

## Secrets at rest

Encryption at rest is **opt-in and off by default.** Configure a master
key and secret values are encrypted with an XChaCha20-Poly1305 AEAD
cipher (`internal/secrets`) before they hit the database. With no key
configured the controller stores secret values as plaintext and logs a
warning at startup. Provide the key via:

- `SPARKWING_SECRETS_KEY` -- a base64-encoded 32-byte key, or
- `--secrets-key-file <path>` -- a file holding the raw or base64 key.

There is no key rotation and no multi-key read path: the controller
holds one key and the stored envelope carries no key id. Swapping the
key makes every previously sealed value unreadable (`GET
/api/v1/secrets/{name}` returns 500), and configuring a key for the
first time against a database that already holds plaintext values fails
the same way. Re-set every secret through the API after changing or
first enabling the key.

Encrypted or not, values leave the server only through the
authenticated secrets API; pipelines read them with `sparkwing.Secret`
(see [sdk.md](sdk.md)).

## Release integrity

GitHub Actions stores `SPARKWING_UPDATE_SIGNING_KEY` as a base64-encoded
32-byte Ed25519 seed or its canonical 64-byte private key. Release jobs sign
the final checksum manifest and every platform asset; the updater embeds only public keys. Rotate the key
through three releases: add the replacement key to the updater trust set and
ship that bridge release with the old signer; change the workflow secret to the
replacement signer; remove the old key from the trust set after supported
updaters trust the replacement. The release gate rejects a signer outside the
embedded trust set. Updaters without the replacement key fail closed rather
than accepting an unknown signer.

## Cache service

`sparkwing-cache` requires a bearer token on its external **write**
endpoints (`--api-token`, falling back to `$SPARKWING_API_TOKEN`); an
empty token disables auth. Read endpoints (clone, file access, repo
listing) are reachable only in-cluster via the Service, not the
ingress. In-cluster callers reach it directly without a token.

Off-cluster runners read Git through the controller's admin-scoped
`/api/v1/gitcache/git/...` proxy. The controller removes its bearer before the
internal request and permits only registration and upload-pack reads. A
login-enabled dashboard exposes that path to machine bearers without accepting
browser session credentials. Direct-cache binary and seed writes use only
`SPARKWING_CACHE_TOKEN`; direct-cache mode never receives the controller bearer.
Keep the raw cache Service private: `pipeline trigger --working-tree` may seed
uncommitted source, and the cache retains up to 128 workspace refs per
repository.

## Container hardening

The Helm charts run the long-lived services as non-root with explicit
`securityContext` settings (the controller as uid 65534, privilege
escalation disabled, all Linux capabilities dropped). The one exception
is the warm-pool warmer: when the pool is enabled the controller
launches an ephemeral `docker:27-dind` pod with `privileged: true` so
it can run dockerd and pre-pull images into a warm PVC. It is
short-lived, single-container, and the only privileged workload
sparkwing creates. See [warm-pool.md](warm-pool.md).

## Verified self-update

`sparkwing update` proves the bytes it installs are the release's bytes
before and after it installs them. The release signs the `SHA256SUMS`
manifest with an ed25519 private key; the updater carries the matching
public key compiled into the binary and verifies the detached
`SHA256SUMS.sig` with pure-Go `crypto/ed25519` -- no external tool and no
network beyond fetching the asset, its detached signature, `SHA256SUMS`,
and `SHA256SUMS.sig`. It
then checks the download against the signed digest, installs atomically,
and re-hashes the installed file, requiring it to equal the verified
digest. macOS binaries are ad-hoc-codesigned by the release *before* the
manifest is hashed, so the verified bytes install unchanged -- nothing is
mutated after verification. A signature, digest, download, or install
failure is terminal: the updater never falls back to `go install`, and a
post-install mismatch restores the prior binary and fails loudly.

The signing key is release machinery, not per-user configuration:

- Generate a base64-encoded 32-byte Ed25519 seed and store it as the
  `SPARKWING_UPDATE_SIGNING_KEY` GitHub Actions secret.
- Add its public key to `internal/releaseauth.TrustedPublicKeys`. The
  release verifier refuses publication unless the secret-derived key is
  in the updater trust set.
- Rotate through the three-release overlap above.
  `SPARKWING_RELEASE_SIGNING_KEY="$SPARKWING_UPDATE_SIGNING_KEY" go run
  ./cmd/verify-release --public-key` prints the secret's public key and
  enforces trust-set membership before release assets are signed.

## Operator checklist

- **Set the auth tokens.** With an empty tokens table the controller
  serves every endpoint unauthenticated. It logs a warning at startup,
  reports `"auth": "disabled"` on `GET /api/v1/health`, and `sparkwing
  cluster status` flags the controller probe as a warning -- fine for a
  laptop, not for a shared deployment. Minting the first token needs the
  controller open (there is no token to authenticate with yet), so it
  bootstraps unauthenticated by design; enable auth by creating an admin
  token and restarting. To make an open controller a hard startup error
  instead -- once you are past bootstrap -- set `SPARKWING_REQUIRE_AUTH=1`
  (or `--require-auth`) so the pod refuses to start with an empty tokens
  table. See [auth.md](auth.md).
- **Terminate TLS at your ingress.** Sparkwing speaks plain HTTP; put it
  behind an ingress/proxy that enforces HTTPS.
- **Pin image digests** rather than floating tags.
- **Encrypt etcd / your secret store.** Kubernetes Secrets are
  base64, not encrypted, unless the cluster enables it.
- **Rotate the GitHub credentials and cache SSH key** periodically.
- **Limit the status token.** Give the controller's `GITHUB_TOKEN` commit-status
  write access only to repositories whose pull requests Sparkwing reports.
