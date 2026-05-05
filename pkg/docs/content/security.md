# Security

How sparkwing protects your code, credentials, and infrastructure.

## Authentication

Every external endpoint requires authentication. There are no
unauthenticated paths that expose user data.

### API (controller)

All controller endpoints require a bearer token:

```
Authorization: Bearer <SPARKWING_API_TOKEN>
```

The root token is stored in a k8s secret (`sparkwing-api-token`) and
injected as an env var. Comparison uses constant-time comparison
(`subtle.ConstantTimeCompare`) to prevent timing attacks.

**Exempt paths** (have their own auth or are intentionally public):
- `/health` — health check for probes
- `/badge` — CI status badge (public by design)
- `/webhooks/*` — GitHub HMAC-verified (see below)

**Rate limiting**: After 10 failed auth attempts from the same IP
within 1 minute, the IP is blocked for 5 minutes (HTTP 429). This
is in addition to nginx ingress rate limiting (30 req/s burst).

### Scoped tokens

In addition to the root token, you can create scoped tokens that are
restricted to specific environments. This enforces least-privilege:
a staging token cannot authorize prod deploys.

**Create a scoped token** (requires root token):

```bash
curl -X POST -H "Authorization: Bearer $ROOT_TOKEN" \
  https://api.example.com/tokens \
  -d '{"name":"staging-cd","environments":["staging"]}'
```

Response includes the raw token (shown only once):
```json
{"id":"abc12345","token":"sw_a1b2c3d4e5f6g7h8i9j0k1l2m3n4","name":"staging-cd","environments":["staging"]}
```

**Token resolution order**: the auth middleware checks the root token
first (constant-time), then looks up scoped tokens via SHA-256 hash
in SQLite. Scoped tokens are stored as hashes, never in plaintext.

**Scope enforcement** happens at three endpoints:
- `POST /authorize` — the pre-deploy authorization check. A staging
  token cannot authorize a prod deploy.
- `POST /trigger` — if the `environment` query param is set, the
  token must be scoped for that environment.
- `POST /secrets`, `DELETE /secrets` — a token can only manage
  secrets for its authorized environments.

**What's not scoped**: read-only operations (list jobs, agents, etc.)
work with any valid token. Runner operations (claim, heartbeat,
complete) use the root token from the k8s Secret.

**Token management** (`/tokens` endpoint):
- `POST /tokens` — create a new scoped token
- `GET /tokens` — list tokens (prefix only, never full value)
- `DELETE /tokens` — revoke a token by ID

Only the root token can manage other tokens.

**Environment values are case-sensitive exact strings.** The value in
the token's `environments` list must match the `environment` parameter
in pipeline YAML or API calls exactly.

### Webhooks

GitHub webhook payloads are verified directly by the controller.
The controller verifies `X-Hub-Signature-256` HMAC using
`GITHUB_WEBHOOK_SECRET` with `subtle.ConstantTimeCompare`
(timing-safe). Only known event types (push, pull_request, ping)
are accepted. 1MB body limit.

### Log streaming

The log service requires a bearer token (`SPARKWING_LOGS_TOKEN`).
SSE clients that can't set headers use `?token=` query parameter
(GET requests only). Token comparison is timing-safe.

### Cache

sparkwing-cache is exposed externally only for code uploads and sync
negotiation. Read endpoints (git clone, file access, repo listing)
are **not routed through the ingress** - they're only reachable
inside the cluster via the k8s Service.

Write endpoints require `SPARKWING_API_TOKEN` as a bearer token.
In-cluster callers (runners, controller) skip auth - they reach
the cache via the internal Service without the `X-Forwarded-For`
header that the ingress sets.

### Dashboard

The web dashboard uses nginx basic auth at the ingress level
(`sparkwing-basic-auth` secret).

## Transport Security

All ingress endpoints enforce TLS redirect (`ssl-redirect: "true"`).
HTTP connections are redirected to HTTPS before any data is exchanged.

Security headers are delivered via application-level middleware
(`securityHeaders` in the controller). This bypasses the nginx
ingress controller's disabled `configuration-snippet` annotations:
- `Strict-Transport-Security: max-age=63072000; includeSubDomains`
  (only when behind TLS / X-Forwarded-Proto: https)
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: DENY`
- `Referrer-Policy: strict-origin-when-cross-origin`

## Network Isolation

### Ingress policies

Default deny with explicit allow rules per service:

| Service | Allowed inbound from |
|---------|---------------------|
| Controller | Internet (webhooks), Web, Runners |
| Cache | Controller, Runners |
| Logs | Controller, Runners, Web |
| Web | Internet (ingress) |

### Egress policies

Runner pods have unrestricted egress (they need to pull dependencies,
push to registries, etc.). All other pods have restricted egress:

| Service | Allowed outbound to |
|---------|---------------------|
| Controller | Cache, Logs, GitHub API, Tempo, Loki |
| Cache | GitHub (SSH, for fetch), upstream package registries |
| Logs | None (receives only) |
| Web | Controller |

## Secret Management

Secrets are stored in AWS SSM Parameter Store and synced to k8s via
ExternalSecrets. No secrets are hardcoded in manifests or images.

| Secret | Purpose | Rotation |
|--------|---------|----------|
| `sparkwing-api-token` | Root bearer token (full access) | Manual |
| `sparkwing-basic-auth` | Dashboard basic auth | Manual |
| `github-config` | GitHub PAT for commit status + gitops push | Manual |
| `ssh-key` | Cache -> GitHub fetch (SSH) | Manual |

## Repository Host Allowlist

The controller restricts which git hosts can trigger pipelines via
webhook. Set `SPARKWING_ALLOWED_REPO_HOSTS` (e.g., `github.com`) to
reject webhook payloads from unknown hosts. This prevents an attacker
from configuring a webhook that clones from a malicious repository.

Local triggers (`wing ... --on prod`) are not affected by this
restriction — they go through the `/trigger` endpoint which serves
authenticated users only.

## Input Validation

### Git refs

All git ref parameters (branch, tag, commit) are validated against
`^[a-zA-Z0-9_./-]+$` with an explicit `..` rejection. This prevents
path traversal and shell injection in git commands.

### Repository URLs

Repository URLs are validated against a strict regex that allows only
SSH (`git@host:path.git`) and HTTPS (`https://host/path`) formats.
No shell metacharacters, no file:// URLs, no local paths.

### Pipeline arguments

Pipeline argument names are validated by `safeEnvName()` before being
injected as environment variables on runner pods. Only alphanumeric
characters and underscores are allowed.

## Container Security

### Security contexts

| Component | Non-root | Read-only FS | No privilege escalation |
|-----------|----------|-------------|----------------------|
| Controller | Yes (65534) | Yes | Yes |
| Cache | Yes (1000) | No (needs /data) | Yes |
| Web | Yes | No (Next.js needs /tmp) | Yes |
| Runner | Yes | No (needs workspace) | Yes |
| DinD | No (root, privileged) | No | N/A |

The DinD sidecar requires `privileged: true` for Docker-in-Docker.
This is an accepted risk, mitigated by pod anti-affinity and network
policies.

## Rate Limiting

Two layers of rate limiting protect against brute force and DoS:

**Nginx ingress** (per-IP):

| Endpoint | Rate limit | Burst |
|----------|-----------|-------|
| Dashboard | 20 req/s | 100 |
| API | 30 req/s | 150 |
| Webhook | 10 req/s | 30 |
| Logs | 50 req/s | 250 |
| Cache | 10 req/s | 30 |

**Application-level** (auth middleware):
- 10 failed auth attempts per IP per minute → blocked for 5 minutes
- Returns HTTP 429 Too Many Requests

## Deployment Recommendations

1. **Set all token env vars** - `SPARKWING_API_TOKEN`,
   `SPARKWING_LOGS_TOKEN`, `GITHUB_WEBHOOK_SECRET`. Without these,
   endpoints fall back to open access with a logged warning.

2. **Set `SPARKWING_ALLOWED_REPO_HOSTS`** — restrict to your git
   hosting provider (e.g., `github.com`).

3. **Pin container image digests** — use SHA256 digests instead of
   floating tags to prevent supply chain attacks.

4. **Enable etcd encryption** — k8s secrets are base64-encoded, not
   encrypted, unless etcd encryption is enabled at the cluster level.

5. **Rotate secrets regularly** — especially the GitHub PAT and SSH
   keys. ExternalSecrets can automate rotation from AWS SSM.
