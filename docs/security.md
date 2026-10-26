# Security

Sparkwing authorizes controller operations, execution claims and stored data
separately. This guide describes the rewrite's security contract. Native
controller, host, storage and deployment bindings still need acceptance evidence.
A fake-backed client test proves transport behavior, rather than a deployed
security boundary. See [rewrite integration](architecture.md).

Report suspected vulnerabilities through
[GitHub's private vulnerability form](https://github.com/sparkwing-dev/sparkwing/security/advisories/new).
The repository's [security policy](https://github.com/sparkwing-dev/sparkwing/security/policy)
states what to include.

## Trust model

The team is the tenant boundary. Within a team, repository authority, secret
scope, declared read edges, reserved pools and claim bindings narrow access.
A name, label or uploaded digest grants no authority by itself.

The controller derives the caller, team, granted runner identity, repository
access, billing provenance and claim fence. It rechecks those facts in the same
transaction as a mutation. The store clock decides whether a holder is live.
A client cannot replace those facts with headers, environment variables or JSON.

Scheduled launches pass only a tenant, stored schedule ID and due instant to the
scheduled-intake transaction. That transaction must recheck current arming consent
and derive the actor, repository, source and replay identity from the stored row.
The controller refuses a missing or wrong-team transaction adapter. Its schedule
tests substitute consent and use real sealed intake. They do not establish the
store's schedule-consent implementation or a working native timer.

Pipeline authors execute code with the permissions of the runner's OS account.
Same-account code may read the runner's private credential file, other files or
process memory. Use a dedicated account, container or VM and enroll only code
that may use those permissions. Private files and filtered process environments
reduce disclosure. They do not isolate hostile processes sharing an account.

Cloud execution runs every Job container as root of its own Kubernetes user
namespace on exclusive team-dedicated nodes; that root maps to an unprivileged
host UID. No Job container is privileged or holds node root. A launcher
credential stays outside the Job. See [Runner Job placement](#runner-job-placement).

An authorized pipeline can use and disclose its secrets. Masking covers values
registered with the host and their supported encodings. Arbitrary transformations,
network exfiltration and files outside the host's output path are outside that
protection. Plan acceptance refuses known registered secrets in job, step, group
and approval IDs because stored identifiers cannot be masked safely. A lazily
issued secret is registered before release without invalidating exact replay of
accepted topology. Every fresh identifier, including generated job IDs, is checked
against issued values as well as declared secrets. One Go host module masks every presentation payload. Stdout and stderr
are masked as byte streams before line splitting. Protocol frames are decoded
first, then their payloads are sanitized before trusted envelopes are built.
The accepted run seals one registration projection containing raw arguments,
secret defaults, effective values and every normalized list item. Stored secrets
and OIDC tokens register with the shared host masker before release. Explicit
`Mask` registrations affect only the registering job and end with its last
process. Child pipeline names, arguments, repository and branch pass the output
secret check before leaving the host.

See [secret values](secrets.md#values-that-can-be-secrets) for the masking
length rule and save-time warning. Empty supplied arguments and defaults carry no value.
Defaults of unselected pipelines still register when they are long enough. Secrets containing `***` are accepted. Replacement markers can
form a registered value containing `***`, or a value containing `*` beside a marker,
across published stream emissions. This rare marker-edge case is accepted.
The old twelve-byte line/list fragment policy
is removed. Remote environment values still use their separate twelve-byte
registration threshold. Plain submitted config is preserved when it is too short
to register a mask. Planning output waits until declarations are known.
Failed discovery withholds output. A healthy selected pipeline can still plan
when a sibling fails discovery, with planning output withheld. Private
declarations drive lookup, flag parsing and routing. Help, display and the
describe cache use a separate presentation type. Incomplete discovery withholds
all unclassified presentation text, including names and job IDs. Help reports the
incomplete-discovery diagnostic. Structured
attributes are masked as JSON text and checked for valid JSON, duplicate keys
and object shape. A failed check
replaces the whole attribute payload. Pipeline code run without a Sparkwing host
is unmasked. See the [masking contract](../internal/secrets/MASKING.md) for
encodings and overlap rules.

## Authentication and authorization

Controller capabilities use `swk_`. Node-host capabilities use `swn_`. Each
contains 256 random bits. Prefixes select an audience and a public metadata
selector. They do not grant a role. Lookup hashes the complete bearer with
SHA-256. Lists and receipts contain metadata, rather than reusable credentials.

Each route declares accepted credential classes, exact grants, binding,
liveness and retry policy in one registration table. `admin` is not an implied
wildcard. Deployment operational credentials cannot replace an operator-console
session. A live caller missing authority gets `403`. A dead credential gets
`401`. Canonical errors carry `error.code`, message, optional hint and details.
See [authentication](auth.md) for the grant and issuance contracts.

### Team boundary route table

HTTP and `api.sock` share route registration and authorization. A team-owned
resource is resolved through the authenticated team. Cross-team access requires
the route's specific service authority. An unregistered route fails closed.
Local peer authentication supplies an account identity and still uses the same
route policy.

Delegation narrows its parent's authority. Award creates one credential for a
selected unit under the claim transaction. Exchange consumes a bootstrap once.
Revocation checks capability and session ancestry, including the requesting
ancestor of a here award. A root that may claim work has no arbitrary execution
write authority.

Sensitive reads need a live bound claim. A terminal job's bounded final log
flush does not reopen general data, source or secret access. A job-process
credential dies with its process. Plan, eval, owner and job bindings retain
their separate permissions.

### Run intake and child calls

A supplied Git SHA must be empty or a full 40- or 64-character hexadecimal
object ID. Revision names and option-shaped values are refused before admission.
API input cannot choose the submitting account or attach verified App facts.

A child call names its live parent job. Only that job can read the child through
its parent relation. The private job snapshot supplies execution inputs, while
public run details keep secret arguments redacted. Admission checks ancestor
pipeline and repository identities within the same team. Re-entering one returns
`409`; a missing ancestor returns `404` without disclosing foreign run facts.

### Sealed run identity

Intake seals each run with the identity it speaks for, derived from facts the
admission transaction verified and never from the request body: a trust class,
the ref, commit, event and pipeline, and GitHub's repository ID for every class
but `asserted`. The classes are `verified-default` (a signed App push to the
default branch), `verified` (any other signed App delivery), `actions` (a run
bound to a GitHub Actions push), `schedule` (a fire of a schedule a team admin
armed, following the default branch GitHub reports at fire time, unlocked and
unmodified, at the commit the App resolves) and `asserted` (every run whose ref,
commit or inputs a principal chose, including schedules a non-admin armed,
locked, overridden or non-default-branch schedules and "run now"). OIDC claims, data and
cache scopes, pool access rules and pipeline-scoped secret release read only
this value.

A job may start only the child pipelines its plan declares with
`StartsChildren`; an undeclared child is refused with `403`. A child either runs
its parent's exact revision (same repository, ref, commit and code commit) and
inherits the parent's identity whole, including its pipeline for OIDC and
pipeline-scoped secrets, or names a branch that the controller resolves and is
sealed `asserted` under its own pipeline name. An asserted child reads only
team-wide shared secrets, like a GitHub reusable workflow called without
`secrets: inherit`. A request that pairs another ref or code commit with its
own commit in the inherited repository is refused with `403`. An explicit
child repository requires authorization from the parent's live claim and always
runs as asserted. Cycle checks compare repository and pipeline together. A
retry keeps its source run's sealed identity, including asserted provenance,
in full and failed modes. Cache, memo and personal placement use that same
writer because that principal chose the code. The retrier is recorded separately
as the actor requesting the retry.
Only the sealed writer's principal can retry a `--runner here` or other local
run. Another member must start a new run because a retry keeps the source
writer's code and data scope.

An Actions push-bound runner takes work only when its sealed provenance proves
the binding's exact repository, ref and commit through Actions or a verified
App delivery. An asserted run cannot use that runner by naming the same ref
and commit.

A `source:` code pin that is a full commit ID keeps the run's class. A branch or
tag pin that does not resolve to the execution commit demotes the run to
`asserted`, because whoever can push that ref chooses the code. The demoted run
still receives its pipeline's scoped secrets; see [secrets](secrets.md).
Bare names must identify a branch or tag. If both exist at different commits,
resolution fails. Use `refs/heads/main` or `refs/tags/main` to choose one.
Commit pins require a full commit ID. Scheduled execution resolves the named
branch in `refs/heads/`, so a tag with the same name cannot select its code.

Reserved pools are granted only to verified App runs, the children and retries
that inherit them, and admin-armed default-branch schedule fires, which receive
only pools whose rules allow the default branch. A typed repository grants no
pool. Access is checked against the rules as they stand at each use: placement,
reserved secret release and OIDC tokens. Removing a rule cuts off every run of
that repository at its next use, including retries of older runs.
Owner-approved extra repositories join the source token only for a verified
push to the default branch.

On Kubernetes, verified default-branch work runs only on fresh, single-use pods.
This includes admin-armed default-branch schedules, plus children and retries
that inherit verified default-branch provenance. Persistent Kubernetes runners
serve other work. With only persistent Kubernetes runners, verified work waits;
add a launcher pool or use Sparkwing Cloud. The
controller reads the platform from the admin-issued enrollment, never from job
claims. Plain-machine runners (VMs and bare metal) keep serving verified work
under the same OS user, so job code can read runner credentials and leave host
state for later jobs. Launcher pods, Cloud pods and Actions job runners are
unaffected.

Child runs nest at most 10 levels deep counting the root, and one root run
uses the team-set descendant ceiling, from 1 to 1000, default 50 at every depth, retries excluded.
Team admins change the ceiling in Team settings or with
`team settings set --max-children-per-root`; changes are audited. Either bound
refuses with `409` and conflict detail `limit` set to `max_child_depth` or
`max_children_per_root`. A child spends the hourly run, flood and queue-depth
budgets of whoever created its root, not the runner holding the parent's
claim. See [limits](limits.md#execution-and-records).

### Approvals

An approval pauses a run until a team member with `approvals.write` (an editor,
admin or owner, including the member who started the run) approves or denies
it. It is a workflow step, not a security control. It does not enforce a
separate reviewer, and it does not narrow what the run's code, secrets or OIDC
tokens can reach. Protect a deployment target with that target's own controls,
such as a protected branch, a reserved pool or the relying party's OIDC claim
checks. Each decision is audited as `approval.decided`.

### Request audit log

Audit records identify the route, operation, team, subject and public credential
prefix. Revocation targets appear after the store commits. Logout and browser
session replacement record `session_digest`, `revoked_account_id` and
`revoked_team` for the session that ended. Identity unlink records
`kept_session_digest` and one audit line per ended session with its digest,
account and team. Every audit field replaces complete Sparkwing credential
shapes with a short SHA-256 fingerprint before truncation. Public 16-character
credential prefixes remain readable. The controller generates every request ID,
ignores client-supplied `X-Request-ID` values and echoes its ID in the response. Source and secret releases record their authorized subject. Raw bearers,
request bodies, captured environment values and secret-bearing hashes do not
belong in logs or receipts. Public run details are redacted display projections.
Approval decisions, run and job cancellations, token minting and rotation,
runner enrollment, and invitation acceptance commit their audit events in the
same transaction. These events contain IDs and closed decision values, without
approval comments, cancellation reasons, email addresses or other personal text.
Each OIDC token a run mints is audited as `oidc.minted` in the transaction that
rechecks the run's authority, naming the run, a digest of its job, the
audience, and the token's `jti`, subject and expiry, so a leaked token traces to
its mint. A token whose audit row does not commit is never returned.
Restricted claims and sealed execution inputs must never be rendered as details.
Run creation and retry return display inputs, not the execution seal. Schedule
declarations, overrides, effective arguments and fire history hide every argument
value because schedules have no describe-time secret classification. Execution
claims retain the original values.

## Login and request budgets

Capability verification uses indexed SHA-256 lookup. Authentication-cache reuse
requires current ancestry and membership generations and the shared five-second
TTL. A cached allow is invalid after the relevant revocation or membership
change. Browser sessions and CSRF checks retain their separate persistence and
verification rules.

## Login budgets

The sign-in routes (`POST /login` and the Google and GitHub flows) carry their
own budgets. One client gets 30 attempts a minute. The listener's existing
budget is 600 per configured hashing slot. Both answer `429` with
`Retry-After` once drained. The account access token posted to `/login` uses
SHA-256 capability lookup and fresh ancestry checks. It does not use the
legacy token table, argon2 verifier or principal cache.

Request limits protect a controller process. Persistent usage counters and
quotas aggregate in the store. The supported deployment has one controller
replica. Coordinated request limiters and cross-replica live-log fan-out are
separate work. Flushed log segments remain the persistence fallback.

### Run submission budgets

The configured hourly flood cap gives each API token its own bucket for
submissions and retries. App deliveries share their team's separate bucket.
Queue shedding measures that team's pending work.
Exhaustion returns `429`; queue shedding returns `503`. Both carry
`Retry-After`. These process budgets complement the store's persistent limits.

Explicit idempotency keys and GitHub delivery IDs identify replays. The store
owns the commit: the first response is `201`, and replay returns `200` with
`replayed: true` and the same run ID. Replay never returns another here bearer
or spends the flood budget. A refused store transaction leaves no replay result.
Identical submissions without an idempotency key create separate runs.

### Limits profiles

An operator profile supplies defaults for its declared guards. Explicit operator
values retain their presence, including zero where the setting allows it.
Inspect effective limits with `sparkwing operator limits show`. Operator ceilings
refuse excessive pipeline values and name both values. They never silently clamp
an author request. See [limits](limits.md) for the setting table and budgets.

A `429` or `503` retains its structured error and any `Retry-After`. Renewal
backoff never counts as a successful renewal. Only operations marked safe by the
route's retry policy can be replayed. A lost claim response or here bearer is
never automatically replayed to recover one-time authority.

## GitHub webhooks

The controller verifies the App webhook's HMAC and deduplicates its delivery key.
A verified default-branch push carries the webhook's repository, ref and SHA.
Acquisition clones exactly that SHA. A later branch-head lookup cannot change
those trust facts or reject a valid earlier push. Retry preserves the original
trust and records its new requester separately.

Source credentials require live authorized claim access. Team Git credentials
also require Cloud work or a shared runner of that team, never a personal machine.
Local owner credentials remain subject to the repository allow-list. Every
credential release is audited. See [Git credentials](git-credentials.md).

## Secrets at rest

Secrets remain encrypted with authenticated row binding. The kind of row
(secret, git credential, submitted run environment or controller setting), its
team, name, pipeline/shared scope and masking policy participate in the binding.
Moving a ciphertext to a row with different access, or to a row of another kind,
cannot make it readable there. A value that fails to open is an error, never an
empty success value.

### Rotating the secrets key

One key seals every team's secrets, git credentials and submitted run
environments, and the session signing key. To replace it:

1. Generate a new key (`openssl rand -base64 32`).
2. Put the current key in the `secrets-key.previous` credential and the new key
   in `secrets-key`, then restart every controller replica. Wait until no
   replica still runs with the old key alone: one that does keeps sealing new
   values under it.
3. Run `sparkwing operator secrets rotate` with a deployment credential holding
   `operator.tokens`, or `POST /api/v1/operator/secrets/rotate` from a console
   session signed in within the last 10 minutes. It reseals every row under the
   new key, one team at a time, and reports counts, `previous_key_only` (rows
   that needed the previous key) and any row neither key opens. It is safe to
   repeat, including after an interruption.
4. Run `sparkwing operator secrets rotate --verify` (`?verify=1`). It writes
   nothing and reports how many rows still open only under the previous key.
   Run the rotation again until it reports `previous_key_only: 0`.
5. When verification reports zero and nothing unreadable, remove
   `secrets-key.previous` and restart every replica. A row reported unreadable
   was already unreadable under both keys; rewrite or delete it first. Back up
   the new key with the database.

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

Container images follow the same rule. The release signs each image digest
with cosign, then moves `vX.Y.Z` onto it with
`docker buildx imagetools create`. `bin/publish-image-tags.sh` resolves every
tag before it moves any of them and fails when one already points at a
different digest, so a `workflow_dispatch` rerun cannot swap bytes under an
operator who pinned the tag and a refusal cannot leave the registry
half-moved; the `force_retag` dispatch input is the only override. A registry
lookup that fails for any other reason stops the step rather than reading as
an absent tag. After the moves it re-reads each tag and fails when it does not
resolve to the pushed digest. The floating `latest` tag is exempt because it is
meant to move. Each release publishes an `image-digests.json` asset naming
every image, its tag, and its digest, built from that re-read, so operators can
pin digests and diff them between releases. The asset carries an
`image-digests.json.sig` signed by the release key, so a swapped listing does
not verify; the cosign signature over the digest is still what proves the image
bytes.

Recovering from a publication-only failure is a choice between two dispatch
inputs. A rerun with `publish_images: false` keeps the images and tags that
already landed, republishes the GitHub release, and omits the
`image-digests.json` asset because no publish job ran. A rerun with
`publish_images: true` rebuilds the images, so the guard refuses the tag move
and the run fails; completing it takes `force_retag: true`, which moves the
version tag onto bytes nobody pinned. Prefer `publish_images: false` unless
the published images are known bad.

## Signed data access

The controller authorizes `/data` signing and logical records. Bytes transfer
directly to filesystem or object storage using short-lived signed requests.
Signed-object clients receive only signed headers and never attach a reusable
controller credential, including to the controller's own origin.

Every record edge checks authority, integrity and existence. An unrelated
same-team or same-run receipt cannot authorize another job's manifest. Each
unverified writer keeps its own data scope, so a member's manual run cannot feed
memo or cache entries to another member or to App, schedule or Actions builds;
see [caching](caching.md#who-shares-a-cache). A remote
cache profile permits only the user's authorized verified-default-branch memo,
artifact and dependency-cache reads, and needs the editor role, because
dependency-cache archives are not checked for secrets. It exposes no write or binary-fetch method.
Publication is separate, labels provenance as local and creates no execution
claim. A publication failure does not change the completed local verdict.

Downloads expire after 60 seconds and verify the digest at EOF. Revocation
cannot recall a signed URL already issued, a GitHub credential or an OIDC token.
Record reachability controls blob retention. Collection marks unreferenced bytes,
waits 24 hours and rechecks before deletion. A live record keeps its bytes even
when the originating run has expired. Missing or corrupt recoverable bytes fail
as an error or cache miss. See [data uploads](data-uploads.md).

## Local daemon socket

The local controller owns its home's store, queue, admission and dashboard.
Unix transports require an owner-only socket directory and kernel peer identity.
The socket directory and Windows pipe names carry a random id kept in the home,
so another account cannot claim them first.
Windows uses the authenticated account SID. An unauthenticated or foreign peer
is refused. A filesystem path alone is not peer authentication.

Same-account processes and root remain inside this trust boundary. Use distinct
accounts and homes where separate users must be isolated. A second store
opener is not part of that contract.
The local dashboard's TCP listener answers only loopback Host names and its own
loopback origin, so a rebound DNS name or another site's page is refused.

TCP carries no peer identity, so the listener also needs a browser session.
`sparkwing dashboard open` mints a sign-in link over the private socket. The
link carries a random 32-byte token, works once and expires after 60 seconds.
Visiting it redirects to `/#s=<session>`. The page moves the session from the
fragment into the tab's `sessionStorage`, clears the fragment, and sends it as
`Authorization: Bearer` on every API request and event stream. No cookie is set
or read: browsers send a cookie to every port on the same loopback host, so
another account's server on `127.0.0.1` could replay it, while a header goes
only to the origin the page names. A TCP request without a live bearer gets
401, except that the static dashboard pages, which carry no run data, load
signed out. The browser then acts as the socket peer that minted its link.
A session ends after 12 hours without a request or 24 hours after sign-in,
whichever comes first, and the dashboard's Log out button ends it at once with
a bearer-authenticated `POST /logout`. The controller holds tokens and sessions
in memory, so a restart also signs every browser out; run
`sparkwing dashboard open` again. The socket itself needs no session.
See [local execution](local-execution.md#the-laptop-boundary).

## Container hardening

The controller and launcher hold service authority outside pipeline Jobs.
Jobs disable service-account token automounting and service links. They receive
a single-use bootstrap through a private file and exchange it for their awarded
unit. No runner root or launcher credential enters a Job's argv or environment.

## Runner Job placement

Every plan, eval and work Job uses the same builder, user-namespaced Pod and
DinD sidecar. The sidecar adds namespaced `SYS_ADMIN`, `NET_ADMIN` and
`SYS_PTRACE` and an unmasked `/proc`, under a Localhost seccomp profile that
every customer container inherits. The kernel's user-namespace surface,
nf_tables above all, is what an escape would use, so node kernel patch
latency is part of this boundary. One Job per team-dedicated node is a hard prerequisite. Jobs from
the same team cannot share a node under this boundary. Aggregate scratch,
including DinD, is fixed at 20 GiB.

Deployment acceptance must prove exclusive node reservation, enforced default-deny
network policy, protected metadata/controller/storage access and complete teardown
before the reservation is released. Labels and rendered YAML alone establish
none of those facts. The launcher creates Jobs without executing pipeline code.
Warm-pool dispatchers and pipeline-owned Kubernetes Job builders are removed
from the final process model.

## Verified self-update

`sparkwing update` proves the bytes it installs are the release's bytes
before and after it installs them. The release signs the `SHA256SUMS`
manifest with an ed25519 private key; the updater carries the matching
public key compiled into the binary and verifies the detached
`SHA256SUMS.sig` with pure-Go `crypto/ed25519` -- no external tool and no
network beyond fetching the asset, its detached signature, `SHA256SUMS`,
and `SHA256SUMS.sig`. It
then checks the download against the signed digest. A signature covers
bytes, not a tag, so it also reads the staged binary's embedded build
identity without executing it and refuses a version other than the requested
release. It then installs atomically,
and re-hashes the installed file, requiring it to equal the verified
digest. macOS binaries are ad-hoc-codesigned by the release *before* the
manifest is hashed, so the verified bytes install unchanged -- nothing is
mutated after verification. A signature, digest, download, or install
failure is terminal: the updater never falls back to `go install`, and a
post-install mismatch restores the prior binary and fails loudly.

The command refreshes managed schedule units and the installed local-controller
service after installation. A success receipt confirms both steps. If the
refresh fails, the diagnostic names the installed CLI version and the retry
command. A build without managed-unit integration refuses before replacement.
`sparkwing update --dry-run` reads release metadata without installing software
or touching services.

The signing key is release machinery, not per-user configuration:

- Generate a base64-encoded 32-byte Ed25519 seed (`openssl rand -base64 32`)
  and store it as the `SPARKWING_UPDATE_SIGNING_KEY` GitHub Actions secret.
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
weekly. The release workflow calls none of it: a tag builds and publishes the
commit those runs already covered on `main`.
- **gosec** over the public module and the `.sparkwing` pipeline module,
  with the rules that describe how a CI tool works (file inclusion and
  subprocess arguments named by its inputs, cache directory permissions)
  excluded. The pipeline writes a repository-relative SARIF file that the
  workflow uploads to GitHub code scanning. The gosec job fails on any
  high-severity, high-confidence finding, so a false positive goes quiet only
  through a source annotation. The scan runs with `-nosec-require-rules` and
  `-nosec-require-justification`, so every suppression reads
  `#nosec GNNN -- <reason>`, naming the rules it silences and why, and no
  `-nosec-tag` alternative is configured, so `grep -rn '#nosec' --include='*.go' .`
  lists every one for review. The comment gate keeps each annotation alone on
  one line, so no free prose rides behind a suppression.
- **govulncheck** in source mode over `./...`, in addition to the
  binary-mode scan the `pre-release` gate runs against every shipped
  executable.
- **gitleaks** over the available git history. `.gitleaks.toml` allow-lists two
  exact documentation and test-fixture values, and `.gitleaksignore` names one
  historical generated-bundle false positive by fingerprint. No path is
  excluded.
- **`npm audit`** over the dashboard's production dependencies at the
  `high` threshold. A registry that times out or answers 5xx is retried, and
  fails as its own error rather than as an advisory. A pass is recorded against
  a digest of `web/package-lock.json` and `web/package.json` and reused for at
  most a day, so an unchanged dependency set is still re-asked daily and an
  advisory is never replayed from the record.

The hosted workflow also runs CodeQL for Go and TypeScript with the
`security-extended` query suite. CodeQL alerts remain report-only. The workflow
pins external actions to commit SHAs, and the three Go-based local scanners use
pinned module versions. The installed npm version and advisory database supply
`npm audit`; CodeQL has no local pipeline step. A pull request or a push to
`main` stops when a scanner cannot complete or when gosec, govulncheck,
gitleaks, or `npm audit` finds a failure. A tag stops for neither, so a
scanner failure on `main` is what holds a release back, before the tag exists.

## Operator checklist

- Provision controller authority through the capability issuer and its private
  credential files. Keep deployment operational grants separate from team and
  operator-console authority. Do not use an unauthenticated bootstrap window.
- Terminate TLS at a trusted ingress and keep signing and source credentials
  outside pipeline Jobs.
- Reserve one Job per team-dedicated node and verify network enforcement and
  teardown with native probes before accepting Cloud execution.
- Configure storage lifecycle rules for the declared log retention classes.
  Startup refuses signing into a class whose lifecycle rule is absent.
- Pin verified image digests, encrypt the deployment's secret store and rotate
  source and release credentials under their owning services' procedures.

## Controller client redirects

Authenticated controller clients refuse redirects because both the credential
and private execution inputs must remain at the selected controller. Supplying
a custom HTTP client does not override that rule. Signed-object transfers use
their own short-lived authority and never inherit the controller bearer.
