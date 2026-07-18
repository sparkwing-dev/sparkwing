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
events (and answers `ping`); other event types are accepted and
ignored.

## Secrets at rest

Secret values are encrypted with an XChaCha20-Poly1305 AEAD cipher
(`internal/secrets`) under a master key, so they are never stored in
plaintext. Values leave the server only through the authenticated
secrets API; pipelines read them with `sparkwing.Secret` (see
[sdk.md](sdk.md)).

## Cache service

`sparkwing-cache` requires a bearer token on its external **write**
endpoints (`--api-token`, falling back to `$SPARKWING_API_TOKEN`); an
empty token disables auth. Read endpoints (clone, file access, repo
listing) are reachable only in-cluster via the Service, not the
ingress. In-cluster callers reach it directly without a token.

## Container hardening

The Helm charts run the long-lived services as non-root with explicit
`securityContext` settings (the controller as uid 65534, privilege
escalation disabled, all Linux capabilities dropped). The Docker-in-Docker sidecar
is the exception: it needs `privileged: true` to build images, an
accepted risk isolated to the build pod.

## Operator checklist

- **Set the auth tokens.** With no token configured the controller
  serves open (it logs a warning) -- fine for a laptop, not for a shared
  deployment. See [auth.md](auth.md).
- **Terminate TLS at your ingress.** Sparkwing speaks plain HTTP; put it
  behind an ingress/proxy that enforces HTTPS.
- **Pin image digests** rather than floating tags.
- **Encrypt etcd / your secret store.** Kubernetes Secrets are
  base64, not encrypted, unless the cluster enables it.
- **Rotate the GitHub credentials and cache SSH key** periodically.
