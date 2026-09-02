# Security

How sparkwing protects code, credentials, and infrastructure.

Report suspected vulnerabilities through
[GitHub's private vulnerability form](https://github.com/sparkwing-dev/sparkwing/security/advisories/new),
not a public issue. The repository [security policy](https://github.com/sparkwing-dev/sparkwing/security/policy)
defines supported versions and the information to include.

## Authentication and authorization

Controller and logs requests carry a bearer token; each route declares
the scope it needs. Tokens are typed (`swu_`/`swr_`/`sws_`), stored as
argon2id hashes, and never logged in full. The complete model -- token
kinds, the scope set, per-endpoint enforcement, the unauthenticated
endpoints, and first-visit admin bootstrap -- is in
[auth.md](auth.md). Sparkwing does not have a "root token"; the `admin`
scope is the superset.

## Login and hashing budgets

`POST /api/v1/auth/login` is the controller's only unauthenticated route
that hashes a password, so it carries its own budgets. One client gets 30
attempts a minute; the listener as a whole gets 600 per concurrent argon2
slot, which bounds hashing work without throttling a fleet's real logins.
Both answer `429` with `Retry-After` once drained.

A failed login also charges a budget keyed on the account **and** the
client address: 5 failures, refilling one every three minutes. Keying it
on both is deliberate. An account-only budget would let any stranger lock
a named user out of the dashboard with 20 requests an hour, so a wrong
guesser only ever slows itself down; the per-client and listener buckets
remain the outer bound on how much guessing one source can do. The budget
charges failures only, so a busy account is never locked out by its
successes.

Bearer verification carries the same protection, keyed on the client
address and the 12-character token prefix, which is public
(`sparkwing cluster tokens list` prints it). Ten failed verifications for one
prefix from one client in a minute and further attempts answer `429`
without hashing. Keying on the pair matters for the same reason it does
for login: a prefix-only budget would let a stranger who reads a prefix
deny that runner its own token on any cold cache. Only a genuine hash
mismatch spends the budget, so a prefix that matches no stored row costs
an indexed `SELECT` and nothing more, and a valid token served from the
principal cache spends nothing at all. The controller also remembers a
rejected raw token for five seconds, so a client replaying one wrong
guess pays for a single hash; that cache evicts its coldest entries when
full rather than closing to new ones.

Every argon2id verification, login and bearer-token lookup alike, passes
through a semaphore sized by `--argon2-memory-budget-mb` (chart:
`controller.argon2MemoryBudgetMB`, default 256). One hash holds 64 MiB
while it runs, so the default admits four at a time. A hash waits at most
250ms for a slot; past that the request is shed with `503` and a
`Retry-After` rather than queued, so a flood cannot grow an unbounded
backlog behind legitimate callers. Raise the budget only alongside the
pod's memory limit. A runner whose token is in the 60-second principal
cache never reaches the store or the semaphore at all, so heartbeats are
unaffected by a login flood.

An unauthenticated caller never sees a store error verbatim. Anything
that is not an authentication rejection answers `503` with a generic
message and the detail goes to the controller log.

Login throttling keys on the TCP peer and ignores forwarded headers until
you name the proxy networks in `--trusted-proxy-cidrs` (chart:
`controller.trustedProxyCIDRs`). The dashboard forwards each browser's
address to the controller, so that list must include the web pod's source
or every dashboard login shares one client budget. Set the web pod's
address where you pin it; where the pod IP is unknown, set the cluster pod
CIDR (`10.244.0.0/16` on kubeadm and kind, `10.42.0.0/16` on k3s) and
accept that any pod in that range can then supply `X-Forwarded-For`. List
the narrowest range that contains the web pod. Leaving it empty stays safe
and turns coarse: every browser then shares the proxy's budget.

## Trigger and list-query limits

`POST /api/v1/triggers` validates `git.repo_url` with the same rules the
Git cache routes use, so a submission cannot point a runner's clone at a
local path, a loopback or private address, or a URL carrying embedded
credentials. It also keeps only the trigger environment keys a run
actually reads (`GITHUB_REPOSITORY`, the GitHub pull-request context, and
the `SPARKWING_START_AT` / `STOP_AT` / `ONLY` / `DRY_RUN` / `NO_CACHE`
switches). Everything else is dropped, including the retry-provenance
keys the controller writes for itself, so a submission cannot forge the
repository directory a later local retry trusts.

`GET /api/v1/runs?limit=` is capped at 1000 rows, in the query parser and
again in the store, so a read-only token cannot ask one request to
materialize every run row with its plan and args blobs.

`GET /api/v1/services` announces internal cache and logs URLs and needs a
bearer; any valid token satisfies it, and every client that consumes it
already holds one.

## Webhooks

GitHub webhook deliveries are verified by the controller: it checks the
`X-Hub-Signature-256` HMAC with a constant-time compare before doing any
work. The handler acts on `push` and on `pull_request` (opened /
synchronize / reopened, against the PR head), and answers `ping`; other
event types and other `pull_request` actions are accepted and ignored.

`GITHUB_WEBHOOK_SECRET` is one value every configured repository holds,
so on its own it says only that *some* holder signed the body -- any
holder could then drive any pipeline against any repository. Bind the
intake with `GITHUB_WEBHOOK_BINDINGS`, a JSON document:

```json
{
  "pipelines": {
    "sample-app-build": {"repos": ["acme/sample-app"], "secret": "..."}
  },
  "repo_secrets": {"acme/sample-app": "..."}
}
```

`pipelines` is keyed by the `{pipeline}` path segment and `repo_secrets`
by repository slug. A slug is lowercased once, when the delivery is
read, and that one value picks the secret and answers the binding, so
no case fold can send the two decisions to different repositories; a
`repository.full_name` that is not an ASCII `owner/name` slug is refused
outright. A pipeline with a `repos` list refuses any delivery naming a
repository outside it, so a repository owner reaches only the pipelines
you bound to them. A `repos` list that is present but empty refuses
every repository; omit the key, or the pipeline entry, to leave the
delivery's repository unchecked. The controller logs the resolved
counts at startup, so an installed document that parsed to nothing is
visible in the log.

The signing secret resolves most specific first -- the pipeline's own
secret, then the named repository's secret, then
`GITHUB_WEBHOOK_SECRET`. Give every bound repository a secret of its own
to isolate them completely: a repository left without one is verified
with the shared secret its peers also hold. In the chart, pass the
document through `controller.extraEnv` from a Kubernetes secret.

A refusal does not say which of these rules it failed. An unbound
repository answers `404`, the same as a pipeline that does not exist,
and once any pipeline or repository carries a secret of its own, a
delivery resolving to no secret answers `401` like a bad signature
rather than `503`. Otherwise the status code alone would enumerate the
binding table and the `repo_secrets` key set, one guess per request.
`503` remains the answer when no secret is configured anywhere.

Each delivery is recorded under two unique constraints: the
`X-GitHub-Delivery` id, store-wide, and a digest of the material the
signature covered -- the pipeline and the request body. The digest is
what closes replay: `X-GitHub-Delivery` is a header the sender picks and
the HMAC does not cover, so keying on it alone would let anyone who
captured one delivery re-send it under an id of their own. Re-sending a
body the controller already accepted answers `409` whatever header rides
with it, and the response names the run the first delivery produced, so
a redelivery from the GitHub side resolves to that run instead of a dead
end. A delivery arriving without the header answers `400`.

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

Each envelope is bound to the fields of the row that decide who may
read it: the secret name, the owning repository (empty for an unscoped
secret), whether an unscoped row is shared with every run, and whether
the value is masked in run output. Anyone with database write access
who copies a ciphertext onto another name, into another repository, or
onto the unscoped row, or who edits a row to widen its own access, gets
a value that fails to open rather than one that answers there.

Values sealed before binding (`enc:v1:` envelopes) still open, and they
are still substitutable until they are rebound. `sparkwing secret list`
reports `BOUND false` for them (`"bound": false` on the API), and the
controller reseals such a row into a bound envelope the first time it
is read, so rows migrate as they are used. Re-setting a secret rebinds
it as well.

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

`sparkwing-cache` requires a bearer token (`--api-token`, falling back to
`$SPARKWING_API_TOKEN`) on every route that touches repository content: git
clone and registration, archives, single files, tree hashes, branch
membership, the repo listing, artifacts, and the blob and sync endpoints. It
refuses to start without one unless the operator passes
`--allow-unauthenticated` (`$SPARKWING_CACHE_ALLOW_UNAUTHENTICATED`), which
logs a startup warning. The guard has no network-location exemption: an
in-cluster caller, a port-forward, and an ingress request are all rejected
without the bearer, because a caller-controlled header cannot prove where a
request came from. `/health`, `/metrics`, `/stats`, and the pull-through
package proxy under `/proxy/` stay open, because package managers fetch
through the proxy without a credential and it serves upstream registry bytes
rather than repository content.

Registering a repository name validates it against
`^[A-Za-z0-9._-]{1,64}$`, and repointing a name that already maps to a
different repository requires the token even on an unauthenticated cache.
Every response carries `X-Content-Type-Options: nosniff`, and artifact
downloads are served as `application/octet-stream` attachments.

Off-cluster runners read Git through the controller's admin-scoped
`/api/v1/gitcache/git/...` proxy. The controller drops the caller's bearer and
presents its own `SPARKWING_CACHE_TOKEN` to the cache, and permits only
registration and upload-pack reads. A login-enabled dashboard exposes that path
to machine bearers without accepting browser session credentials: the mount
rejects a request carrying no bearer credential before it extends the
half-hour stream deadline or proxies anything, and caps concurrent Git streams
so one caller cannot hold every long-lived connection. Direct-cache binary and
seed writes use only `SPARKWING_CACHE_TOKEN`; direct-cache mode never receives
the controller bearer.

The runner-bundle chart ships a default-deny ingress NetworkPolicy for the
cache pod (`networkPolicy.enabled`, on by default). It admits the release's
runner, controller, and dashboard pods plus the Job pods the Kubernetes runner
backend creates (`app.kubernetes.io/name: sparkwing-runner`), and refuses to
render a non-`ClusterIP` cache Service unless a token Secret is configured. A
controller or runner pool outside the cluster reaches the cache through
`networkPolicy.extraIngress`, which is appended to the rule verbatim and takes
an `ipBlock` for the caller's source range.

`pipeline trigger --working-tree` may seed uncommitted source; the cache
retains up to 128 workspace refs per repository and expires them after
`WORKSPACE_SEED_MAX_AGE` (24 hours by default). Expiry moves the ref into
`refs/sparkwing-workspace-archive/` rather than dropping it, so a retry of an
older working-tree run still finds its snapshot; archived refs are dropped
after seven times `WORKSPACE_SEED_MAX_AGE`, or once 128 of them accumulate.

The cache's unauthenticated `/metrics` carries no per-repository label, so
scraping it does not enumerate or confirm the mirror set.

## Local daemon socket

The admission daemon (`wingd`) is a per-user process on the developer's
own machine. It serves a unix socket at
`/tmp/sparkwing-<uid>-<hash>/d.sock`, where the hash covers
`SPARKWING_HOME`. The path is a pure function of the home: no
environment variable moves it, so a cron job, a privilege-elevated
shell, and an interactive session all resolve the same socket for the
same home. It sits under `/tmp` rather than under the home because a
unix socket path is capped at 104 bytes on macOS. Windows uses the
process temp directory instead. The trust boundary is the user account,
not the machine: everyone logged into the same host as the same user
shares one daemon and can queue, inspect, cancel, and drain its runs.
The protocol carries no token, and adding one would not change that -- a
token readable by the account is readable by anything running as the
account.

Other accounts on the host are outside the boundary, and the checks
below keep them out. The base directory must be a directory carrying the
sticky bit, or else not be writable by other accounts, so no one can
rename this user's socket directory away and substitute their own. The
daemon then creates its socket directory with `Mkdir` and refuses to
serve if the path already exists as anything but a real directory owned
by the current uid with mode `0700`, so another account cannot
pre-create it and collect connections; a foreign directory at that path
is a refusal that names it, never a redirect somewhere else. The bound
socket is chmodded to `0600`. Every accepted connection is checked
against the kernel's peer credentials (`SO_PEERCRED` on Linux,
`LOCAL_PEERCRED` on macOS and FreeBSD) and dropped when the caller's uid
differs, which holds even where socket file modes are not enforced on
connect. Clients apply the same base and directory tests before dialing,
including the peer sweep behind `sparkwing doctor`, so a `sparkwing`
command refuses to hand a handshake to a socket sitting in a directory
this user does not own.

The ownership, mode, and peer-credential checks are unix-only. Windows
reports no uid for a unix socket peer and has no sticky bit, so the
per-user temp directory is the only separation there, and the daemon
neither refuses a connection on credentials nor sweeps a stale socket
directory away.

Root is not excluded by any of this; a root account on the host can read
the daemon's memory whatever the socket says. On a shared host, give
each user their own `SPARKWING_HOME`, which is the unit of daemon
isolation.

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

## Static analysis

The `security-scan` pipeline runs four local scanners. The Security GitHub
Actions workflow runs it on every pull request, on pushes to `main`, and
weekly. The release workflow calls the same workflow against the resolved tag
commit and waits for every security job before building release artifacts.

- **gosec** over the public module and the `.sparkwing` pipeline module,
  with the rules that describe how a CI tool works (file inclusion and
  subprocess arguments named by its inputs, cache directory permissions)
  excluded. The pipeline writes a repository-relative SARIF file that the
  workflow uploads to GitHub code scanning. Gosec findings remain report-only
  until the pipeline runs with `--strict`; they do not block a pull request or
  release by themselves.
- **govulncheck** in source mode over `./...`, in addition to the
  binary-mode scan the `pre-push` gate runs against every shipped
  executable.
- **gitleaks** over the available git history. `.gitleaks.toml` allow-lists two
  exact documentation and test-fixture values, and `.gitleaksignore` names one
  historical generated-bundle false positive by fingerprint. No path is
  excluded.
- **`npm audit`** over the dashboard's production dependencies at the
  `high` threshold.

The hosted workflow also runs CodeQL for Go and TypeScript with the
`security-extended` query suite. CodeQL alerts remain report-only. The workflow
pins external actions to commit SHAs, and the three Go-based local scanners use
pinned module versions. The installed npm version and advisory database supply
`npm audit`; CodeQL has no local pipeline step. A release stops when a scanner
cannot complete or when govulncheck, gitleaks, or `npm audit` finds a failure.

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
- **Point the logs service at a controller.** Without `--controller`
  (`SPARKWING_CONTROLLER_URL`) `sparkwing-logs` resolves no tokens, so
  anything that reaches its Service can read, forge, and delete every
  run's logs. It reports `"auth": "disabled"` on `GET /api/v1/health`
  and `sparkwing cluster status` flags the logs probe as a warning. Set
  `SPARKWING_REQUIRE_AUTH=1` (or `--require-auth`) so the pod refuses to
  start without an absolute `http(s)` controller URL, which keeps a
  typo from advertising `"auth": "enabled"` on a service whose every
  token lookup fails. The runner-bundle chart wires the controller URL
  from `controller.tokenSecret`, and a logs-enabled install without that
  Secret fails at render time unless you set
  `logs.allowUnauthenticated=true`. `cluster status` warns rather than
  passing whenever it cannot read the logs service's auth state: no
  announced logs URL, a health body with no `auth` field (an image
  older than the report), or a degraded service.
- **Size the logs service's quotas for your volume.** `sparkwing-logs`
  caps what one authenticated runner can spend. Each flag below reads an
  environment variable of the same meaning, and `0` turns that bound off.

  | Flag (env) | Default | Effect |
  |------------|---------|--------|
  | `--max-node-bytes` (`SPARKWING_LOGS_MAX_NODE_BYTES`) | 64MiB | Stored-byte cap for one node's log. Appends past it store a `[sparkwing-logs] truncated` marker once and are then dropped with `204`. |
  | `--max-run-bytes` (`SPARKWING_LOGS_MAX_RUN_BYTES`) | 1GiB | Same cap across every node log in one run. |
  | `--min-free-bytes` (`SPARKWING_LOGS_MIN_FREE_BYTES`) | 512MiB | Free space on the volume below which appends are rejected with `507`, leaving room to read and delete what is already stored. |
  | `--retention` (`SPARKWING_LOGS_RETENTION`) | 0 (off) | Age after a run's last write at which the sweeper deletes its logs. Off by default so an upgrade deletes nothing; `168h` is a common choice. |
  | `--sweep-interval` (`SPARKWING_LOGS_SWEEP_INTERVAL`) | 1h | How often the sweeper runs. |
  | `--search-max-bytes` (`SPARKWING_LOGS_SEARCH_MAX_BYTES`) | 256MiB | Bytes one `GET /api/v1/logs/search` may read. |
  | `--search-timeout` (`SPARKWING_LOGS_SEARCH_TIMEOUT`) | 10s | How long one search may scan. |

  A search that hits either budget, or whose caller disconnects, returns
  the matches it found with `"truncated": true`. Search also requires
  `run_id`; a query without one is refused with `400` rather than
  walking every stored run.

- **Terminate TLS at your ingress.** Sparkwing speaks plain HTTP; put it
  behind an ingress/proxy that enforces HTTPS.
- **Pin image digests** rather than floating tags.
- **Encrypt etcd / your secret store.** Kubernetes Secrets are
  base64, not encrypted, unless the cluster enables it.
- **Rotate the GitHub credentials and cache SSH key** periodically.
- **Limit the status token.** Give the controller's `GITHUB_TOKEN` commit-status
  write access only to repositories whose pull requests Sparkwing reports.
