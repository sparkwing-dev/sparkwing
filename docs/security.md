# Security

How sparkwing protects code, credentials, and infrastructure.

Report suspected vulnerabilities through
[GitHub's private vulnerability form](https://github.com/sparkwing-dev/sparkwing/security/advisories/new),
not a public issue. The repository [security policy](https://github.com/sparkwing-dev/sparkwing/security/policy)
defines supported versions and the information to include.

## Trust model

Read this before deciding who holds a token and which repositories share a
deployment.

**The default controller has one team. A license enables multiple teams.** In the default mode, projects share the `default` team's authority.
A controller with a valid multi-team license requires authentication and
scopes tenant API requests and store operations to the authenticated team.
Run lists use that team, and a run belonging to another team is answered as
not found. See [Teams and sign-in](auth.md#teams-and-sign-in).

Within a team, scopes and member roles decide which operations a caller may
perform. Repository and environment names do not create separate tenant
boundaries inside that team. Repository and git ref do bound a run's cache
writes: a run's cache and binary entries are written under its own repository
and ref, as [Cache service](#cache-service) describes.

**Operators and service credentials have separate authority.** `admin` is
deployment authority. Service scopes such as `credits.grant`, `claims.launch`,
and `logs.delete` cross teams for their documented operations. Team membership
does not grant them. Operators who control the database, encryption keys or
cluster remain trusted with tenant data. See [Authentication](auth.md) for
individual scope and claim contracts.

**A self-hosted runner runs pipeline code as its own OS user; give it a
dedicated one.** Pipeline code on a runner or agent can read every file that
user can: the agent's `config.yaml` and the runner token in it, ssh keys,
cloud profiles and other repositories' secrets. No Sparkwing setting changes
that, the same as GitHub's self-hosted runners. Run each runner as a
dedicated OS user that holds nothing else, or in a container or VM, and list
only repositories whose every committer may act as that user.

**Pipeline authors run code on runners.** A pipeline is Go that the runner
compiles and executes. Enrolling a workstation or gateway authorizes that
code to execute as the agent service's OS user. Assisted execution keeps the
enrollment bearer and claim identity in the supervisor; the job-body child
gets a process-lifetime loopback capability limited to its exact run and node,
including reserving and committing its own typed output upload,
with execution start, finish, and logs additionally bound to its acknowledged
attempt ordinal. The child does not inherit arbitrary agent service credentials,
and its capability cannot claim or renew work, manage the fleet, or call
administrative routes. A claimed trigger's pipeline binary, which plans the
run, gets the same kind of loopback capability, limited to its own run and
the child runs it spawns, from a runner using the in-process node runner.
The broker names the run on every route it forwards, so a runner holding two
runs never lets one read the other. Two lookups reach beyond it, both scoped
to the team by the controller: a cross-pipeline `Ref` reads another
pipeline's latest run and then only that run's node outputs, and a retry
asks which child its source attempt spawned so it reuses that child. The latest-run lookup
admits any pipeline in the team, not only a declared `Ref`; that is the trust
the pipeline's own code already has, since it can reference any team
pipeline's latest run. A runner using the Kubernetes or
warm node runner still hands its token to the trigger's binary, because the
Jobs that binary creates need it; controller dispatch (`sparkwing-runner
launch`) instead gives each Job a claim token for its assigned work.

**Hosted execution also depends on the cluster configuration.** The launcher
keeps its service credential outside the Job. Its Job builder disables
service-account token automounting, uses a non-root user and a runtime-default
seccomp profile, and gives the Job scratch storage. These settings depend on
appropriate Kubernetes RBAC, enforced network policies, protected controller
and storage credentials, and controlled volume mounts. A multi-team license
does not configure those protections or isolate pipeline code from a shared
host's OS permissions.

**A warm runner's pool token belongs to an operator-only team.** A warm
runner claims triggers with its pool token, and only for that token's team,
then runs that team's pipeline code in its own pod or in Jobs it creates. That
code can read whatever the runner holds: the pool token itself, a GitHub token
the runner carries for private modules, the service account it creates Jobs
with, and the cloud role attached to that account, which the cloud's identity
webhook still injects into Jobs that disable the Kubernetes token. Creating a
Job in the runner's namespace can mount any Secret there. Every member of the
pool token's team therefore has operator authority over that namespace and
role, so put nobody but operators in it. Customer pipelines belong on runners
whose token's team they own, launched through controller dispatch, which gives
each Job a claim token for its own work and no Kubernetes API token.

**Cache and source sharing have their own provenance requirements.** A source
URL and a cache grant alone do not establish that every Git object in a shared
mirror is public. Assess imported and seeded objects separately from
controller API permissions. This guide makes no tenant-isolation guarantee
for those shared mirrors.

Assisted execution on a self-hosted machine keeps the agent OS user's file,
network, and process permissions. It can read the agent's
`config.yaml`, which holds the runner token, and every other credential in
that user's home, such as ssh keys and cloud profiles. GitHub gives the same
warning for its self-hosted runners. Run the agent as a dedicated OS user
that holds nothing else, or in a container or VM, and admit only repositories
whose every committer may act as that user. Sparkwing does not
join a tailnet or configure host networking.
[threat-model.md](threat-model.md) states what that boundary isolates, what it
does not, and what an operator does about the rest when a teammate's branch
runs on an enrolled desktop.

Native Windows helpers start the body suspended, assign it to a kill-on-close
Job Object, and then resume it, so nothing the body spawns exists outside the
job. The supervisor waits for the Job to report zero active processes after
the body exits or is cancelled. Linux, macOS, and WSL helpers instead create a
dedicated process session, send TERM and then KILL to its remaining members,
and wait for that session to empty. Unix code can call `setsid` to leave that
accounting boundary. Pipeline bodies must not daemonize out of their session;
Sparkwing does not try to contain intentional evasion by code already trusted
to run as the agent OS user.

Schema 30 supplies authenticated foreground enrollment, offer, award, source
handoff, and coordinator fallback as an internal release dependency. It does
not expose remote helper body completion. The containment boundary above must
ship with schema 31, which adds the current-attempt fence and durable grants
required by `Memoize`, `Concurrency`, `ToolSlot`, `RunAndAwait`, cross-pipeline
references, and dynamic `SpawnNode`. Schema 30 is not a complete assisted-node
compatibility boundary on its own.

**A warm pool shares one OS account across repositories.** Assisted job bodies
run in separate child processes, but consecutive nodes still share the same
filesystem, network identity, and OS permissions. A pipeline that writes a
credential to disk can leave it where the next repository reads it.
`sparkwing cluster worker --runner k8s` gives nodes separate Job pods, while
its planning process still has the runner-token authority described above.
See [warm-pool.md](warm-pool.md) for that execution mode.

**`runs.read` applies within the caller's team.** `GET /api/v1/runs`
combines the caller's filters with the authenticated team. A team reader can
read that team's pipeline and repository history, including permitted plans,
events, trends and queue views. Secret argument values are masked, and some
fields have narrower permissions, such as dispatch environment snapshots.
Give this scope to someone who may read the team's history. An operator's
`admin` authority is separate from this team-scoped read permission.

**Accepted trust facts.** Each of these follows from the model above and is
not a defect. A team editor can read masked secret values: it can mint a
runner token, start a run, claim it and read the run's secrets, because it
already chooses the code that run executes. The operator's launcher token
receives claim tokens for every team's ready nodes, so whoever holds it reaches
every team's run secrets, source tokens and cache grants; keep it
operator-only. Which secrets a run may read through a claim token is declared
by that run's own plan, so the boundary is the secret's pipeline or shared
scope, not the declaration. The masker covers step output that goes through
the exec helper and the node log; anything else a pipeline process prints to
its own stdout reaches the operator's container logs unmasked. `docker run -e
K=V`, which the SDK's docker and services helpers use, puts the value on the
docker CLI's command line, which every account on a
shared self-hosted host can read in the process table.

**The laptop dashboard admits the account that started it.** `sparkwing
serve start` mounts the controller API and the dashboard on one listener
and requires the token in `serve-token` under the Sparkwing home, a `0600`
file, so another account on the machine that reaches the loopback port is
refused. The CLI sends the token as a bearer; a browser signs in once
through the link `sparkwing serve status` prints, whose single-use code the
page trades for a session it keeps in `localStorage` and sends as a bearer.
A session has no expiry and survives restarts; deleting `serve-token` and
restarting the dashboard ends every browser session.
No credential rides a cookie, which every port on `127.0.0.1` would receive. The process refuses a
non-loopback `--addr` unless the operator passes `--allow-remote`, and a
browser request carrying a foreign `Origin` is refused unless the operator
named that origin in `--allow-origin`. See
[local-execution.md](local-execution.md#the-laptop-boundary). The
cluster-mode controller is the authenticated deployment; laptop mode is a
single-user tool on a single-user machine.

## Authentication and authorization

Controller and logs requests carry a bearer token; each route declares
the scope it needs. Tokens are typed (`swu_`/`swr_`/`sws_`), stored as
argon2id hashes, and never logged in full. The complete model -- token
kinds, the scope set, per-endpoint enforcement, the unauthenticated
endpoints, and first-visit admin bootstrap -- is in
[auth.md](auth.md). Sparkwing does not have a "root token"; the `admin`
scope is the superset.

### Request audit log

The controller writes one slog record per request. A read is an `http` record
carrying the request id, method, route pattern, status and duration. A write
is an `audit` record that adds `ts`, `client_ip`, `client_class`,
`principal_kind`, `principal_id` (an account id or a token prefix, never an
email or a token) and `team`. These also get an `audit` record:

- reads of secrets, tokens and operator routes (`/api/v1/secrets`,
  `/api/v1/tokens`, `/api/v1/team/runner-tokens`, `/api/v1/team/cli-tokens`,
  `/api/v1/operator/...`);
- every `401` and `403`, whatever the method. A refused bearer shaped like a
  Sparkwing token adds `attempted_prefix`, the prefix that names the token
  without granting it.

Identity changes name their target: `member_id`, `invitation_id`,
`token_prefix`, `secret_name` and `role`. A target taken from the request is
recorded only in the shape the store uses: a token as its prefix, even when a
whole bearer was sent, an id only when it looks like one the store mints, and a
role only when it is a valid role. A secret name is recorded once the secret
was found or written. No record carries the raw path, query, body, headers or a
credential.

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

Login throttling keys on the TCP peer. Only a request accepted on the
controller's `--trusted-proxy-addr` listener counts against the budget of its
`X-Real-IP`, so keep that port reachable by the ingress and the dashboard alone.
Without it every browser shares the proxy's budget, which stays safe and turns
coarse. [auth.md](auth.md) covers the setup.

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

The clone-URL check canonicalizes the host before it decides: a trailing
dot, an IPv6 zone id, and the decimal, hexadecimal, and octal spellings of
an address (`127.1`, `2130706433`, `0x7f000001`, `017700000001`) all
resolve to the same place a resolver sends them, and loopback, private,
link-local, carrier-grade NAT, and the cloud metadata names are rejected
in every spelling.

An scp-like URL is read the way ssh reads it. `git@a@127.0.0.1:repo.git`
is refused rather than checked as a host named `a@127.0.0.1`, which is
what the guard used to do while ssh, splitting the destination at the
last `@`, dialled the loopback address. A host in that form must read as
a hostname, with no `@`, `:`, or other character in it that would let the
host checked differ from the host dialled.

A host is also refused when it sits in a name space that only ever points
inward: `internal`, `local`, `localdomain`, and `home.arpa`, whole or as
a suffix, beside `localhost`, the cloud metadata names, and the
`ip6-localhost` and `ip6-loopback` aliases every Debian and Ubuntu
`/etc/hosts` ships.

**This is a name check, not an address check, and it is not a complete
SSRF guard.** A name that resolves to a loopback or private address --
`evil.example.com` pointed at `127.0.0.1` -- passes it. Names are not
resolved during validation on purpose: `git` resolves the name again when
it connects, so an address checked here is not the address reached, and a
`Host` alias in the runner's `~/.ssh/config` can send ssh somewhere the
name never resolved to at all. Bound clone targets where the connection
is actually made: an egress network policy on the cache and runner pods
that permits only the forges you clone from. Deployments that build
sparkwing can also install a host allowlist through the clone validator's
host-policy hook, which runs after every check above.

`GITHUB_REPOSITORY` is the one submitted key a runner reads as a clone
target, and it wins over `git.repo_url`, so it is accepted only as an
`owner/name` slug. A caller without `admin` also cannot submit
`trigger.source: github` or the pull-request environment keys: those are
what the commit-status reporter trusts when it spends the controller's
GitHub token, and the HMAC-verified webhook is what writes them.

`GET /api/v1/runs?limit=`, `GET /api/v1/triggers?limit=`, and
`GET /api/v1/runs/{id}/events?limit=` are capped at 1000 rows, in the
handler and again in the store, so a read-only token cannot ask one
request to materialize every row with its plan, args, and payload blobs.

`GET /api/v1/services` announces internal cache and logs URLs and needs a
bearer; any valid token satisfies it, and every client that consumes it
already holds one.

## Flood control

A push storm, a bot opening hundreds of pull requests, or a misconfigured
hook delivers thousands of events in minutes, and each one costs a run, a
log object, and a row. Three controller settings bound what one burst can
create. All three default to off, so a controller that names none admits
what it always did.

`--max-runs-per-principal-hour N` (chart
`controller.maxRunsPerPrincipalHour`) caps the runs one principal may
create in a rolling hour. An authenticated submission spends its own
token's budget, or its team's for any team but the operator's. A
[GitHub App](github-app.md) delivery carries no principal, so it spends
the budget of the team its installation is bound to, once for every run
it creates. Past the cap the controller answers
`429` with a `Retry-After` naming the real refill delay, which lengthens
while a caller keeps knocking at an empty budget. The budget lives in
controller memory, so a restart or a rollout refills every principal;
it bounds a burst, not a month.

`--shed-queue-depth N` (chart `controller.shedQueueDepth`) answers `503`
with a `Retry-After` once the submitting team's pending triggers reach N,
which is the outer bound on how deep a backlog one burst can grow. Each
team's queue is measured on its own, so one team's backlog never sheds
another team's submissions. The depth is read at most once a second per
team, because a flood asks for it far faster than it changes.

`--trigger-dedupe-window D` (chart `controller.triggerDedupeWindow`)
answers a content-identical `POST /api/v1/triggers` submission inside D
with `409` and the run the first one started. The digest covers the
submitting principal, so a `409` naming a run id only ever reaches the
principal that owns that run and two tenants submitting the same body get
a run each. A GitHub App redelivery is deduped regardless: the
controller keeps the digest of every signed body that started runs for
90 days, apart from the runs, so a retried delivery answers `duplicate`
with the runs it already started whatever this window says, and one
whose runs were deleted starts nothing. A delivery whose event is older
than those 90 days is refused (see [GitHub App](github-app.md)).

Deduplication runs before the shed and the cap, so a redelivery is
answered with its original run rather than a refusal, and retrying one
never spends the submitter's budget.

Every refusal is a status a caller can act on and a line in the
controller log at warn naming the principal and the reason. Nothing is
dropped silently.

## Per-runner request budgets

The claim and heartbeat routes can carry a budget of their own, because a
looping runner reaches them thousands of times a minute without ever
failing authentication. `--claims-per-runner-minute` and
`--heartbeats-per-runner-minute` (chart
`controller.claimsPerRunnerMinute`,
`controller.heartbeatsPerRunnerMinute`) bound what one runner spends per
rolling minute. Both default to zero, which is unlimited: an operator
opts in.

A claim that comes back with a node spends no claim budget. An award is
work the controller chose to hand out, and the loop that gets one
re-claims at once rather than waiting its poll interval, so charging it
would bound how fast a runner may execute rather than how fast it may
ask. What the budget bounds is empty polling, which a runner can do
without limit: a pool runner polls every 500ms, or 120 a minute, so 480
allows four times that cadence. Node claim heartbeats carry no such
exemption; 1200 suits the 3s cadence the shipped runners keep. Run and
trigger claim heartbeats are unbudgeted because losing either can reap
an active run.

The budget is keyed on the runner, not the token. The controller derives
the runner from the route wherever it can -- the node, run, or agent the
path names -- and falls back to the `X-Sparkwing-Runner` header only on
`POST /api/v1/nodes/claim`, `POST /api/v1/nodes/claim/prepare` and
`POST /api/v1/triggers/claim`, which name nothing. A runner sends one
identity for the life of its process (a pool runner its holder prefix and
process id, an enrolled agent its name), not one per poll: a value that
changed per request would buy a fresh budget on every claim and grow the
controller's bucket table at the fleet's poll rate.

On those three claim routes the identity is the runner's own word, so a
holder of a valid token that varies it gets a fresh budget each time.
`--runners-per-token` (default 64) caps how many such names one caller --
its team for a signed-up team, its token in the operator's team -- may hold
at once. A name counts until it has gone unused for ten minutes, a name
already held keeps working, and a new name past the cap is answered `429`
naming the cap and counted under
`sparkwing_principal_throttled_total{route_class="runner_names"}`. So varying
the name multiplies a caller's budget at most that many times. Size the cap
above the largest fleet that shares one token. The per-token budget below
bounds the caller's total, and the token itself is the control that ends it
-- revoke it.

A runner too old to send an identity shares one bucket with its peers on
those routes, so during a rolling upgrade a shared-token fleet is
budgeted as one caller there. Size the budgets per runner and the older
half of the fleet still clears them, or leave the budgets at zero until
the rollout finishes.

`--limits-profile` turns the budgets on as a set, so a hosted controller
carries one setting rather than one per guard. It is described under
[Limits profiles](#limits-profiles) below.

The agent liveness heartbeat, `POST /api/v1/agents/{name}/heartbeat`,
is never shed. Run and trigger heartbeats and node touch requests are
unbudgeted when the caller owns the claim. Losing those updates can
tear down an active run or executor.

Past a budget the route answers `429` with a `Retry-After` naming the
real refill delay, and `sparkwing_principal_throttled_total{route_class}`
counts it. A runner reads a `429` the way it reads a `503`: it waits the
header out, capped at 30 seconds, and keeps its claim and its node. A
host's own admission daemon and the loopback controller budget nothing,
because their callers are unauthenticated and would share one bucket.

## Per-token request budget

`--requests-per-token-minute` bounds ordinary and signed-data requests,
keyed by signed-up team or by token prefix in the operator's team. It binds
a caller varying the runner it says it is: on the two claim routes the runner name
is the caller's own word, so the per-runner budgets above bound a runaway
loop rather than a holder of a valid token who means harm. The agent
liveness routes above are spared here too. Past the budget a request answers
`429` with a `Retry-After`, counted under
`sparkwing_principal_throttled_total{route_class="token"}`.

`--requests-per-minute-alarm` refuses nothing. It is the rate, across
every caller, past which the controller logs at warn and counts
`sparkwing_request_rate_alarm_total`, once a minute: the notice that one
pod is serving more than it was sized for.

## Idle-poll enforcement

A controller told to enforce its idle-poll suggestion answers a claim
poll that arrives sooner than the widest interval it suggests with `429`
and a `Retry-After` naming the rest of the wait, instead of a claim.
Enforcement starts only once that widest interval has been the standing
suggestion for a whole interval, so a runner is never refused against an
interval it was not yet told about, and it lifts the moment work is
handed out, so a fleet is never held off a queue that has since filled. A
runner that honors the suggestion waits at least that long by
construction and is never refused; a runner that ignores the header pays
the wait it was told about, which is never longer than one suggestion, so
two refused polls and a runner's own spread still fit inside the
placement hold. A controller that suggests nothing, which is
any controller with `--idle-claim-poll=0` and every host's own admission
daemon, enforces nothing.

The gate guards the two routes that carry the suggestion, `POST
/api/v1/nodes/claim` and `POST /api/v1/triggers/claim`. A route that
names the trigger or the node it wants is no idle poll, and the
preparation half of an offer round would charge the round twice. It is
keyed the way the per-runner budgets are, on the token prefix together
with the runner, and the runner a pool names itself carries its process
id, so two runner processes on one host are two runners rather than one
polling twice.

Before turning enforcement on, check what the fleet is running. A caller
older than v0.50.1 sends no `X-Sparkwing-Runner`, so every such caller on
one token shares a single gate slot and all but the first are refused
every round. They hold their claims and their nodes, because a `429` is
backpressure they already honor, but they pick work up no faster than
one runner would. Roll the fleet forward first, or leave
`--idle-claim-poll` at zero until it is.

Enforcement is off unless a limits profile turns it on;
`sparkwing_principal_throttled_total{route_class="idle_poll"}` counts the
refusals.

## Limits profiles

`--limits-profile` (chart `controller.limitsProfile`) names a set of
abuse guards a hosted controller runs with, so provisioning writes one
setting rather than one per guard. Empty, the default, supplies none: a
self-hosted controller keeps every budget unlimited and enforces no idle
poll, which is what it served before profiles existed.

| Guard | `cloud` | `cloud-free` |
| --- | --- | --- |
| `--claims-per-runner-minute` | 480 | 240 |
| `--heartbeats-per-runner-minute` | 1200 | 600 |
| `--requests-per-token-minute` | 3600 | 600 |
| `--requests-per-minute-alarm` | 5000 | 5000 |
| `--egress-max-log-streams` | 50 | 10 |
| `--egress-max-downloads` | 20 | 5 |
| `--max-runs-per-principal-hour` | 600 | 60 |
| `--shed-queue-depth` | 5000 | 1000 |
| `--egress-daily-alarm-bytes` | 200 GiB | 20 GiB |
| Idle-poll enforcement | on | on |

The claim budgets are worked from the cadence the shipped claim loop
keeps rather than from a round number. It polls once every 500ms while
the queue is empty, which is 120 requests a minute, and claims once more
for each node it starts: `cloud` allows four times that cadence and
`cloud-free` twice, so a runner keeping its configured cadence is never
refused and one stuck in a tight loop is held to about the work it was
asked to do. The heartbeat budgets carry the shipped cadence with the
same headroom the recommendation above uses, and the free tier halves the
paid figure. The per-token budgets carry the rest of what a runner
spends: a two-slot runner honoring its cadences spends roughly 300
requests a minute once its node heartbeats and state writes are counted,
and a signed-up team's tokens share one budget, so the free tier carries
one such runner and the paid tier a pool of ten with a fifth to spare. The alarm is what one controller pod is sized to serve. The
egress caps are the concurrency one team is expected to read logs and
artifacts at.
The run cap bounds the pending triggers one principal can queue when no
runner claims them, and the shed depth is the fleet's backstop behind it.
The egress alarm names the day the controller's traffic runs ahead of the
pod it was planned for; it refuses nothing. The bill one team can run up
with no compute at all is bounded by that team's daily download cap,
`--team-daily-download-free-bytes` and `--team-daily-download-funded-bytes`,
which applies with or without a profile.

A profile fills a guard only where the command line and the environment
named none, and a guard the operator named wins whatever its value,
including an explicit zero that turns it off. Raising one guard on a
hosted controller is one flag beside the profile rather than a fork of
it. `sparkwing cluster limits show` prints the budgets in force beside
the stored compute guards.

## GitHub webhooks

The [GitHub App](github-app.md) is the only signed GitHub trigger. Its
deliveries arrive at `POST /webhooks/github-app`, which checks the
`X-Hub-Signature-256` HMAC against the App's webhook secret with a
constant-time compare before doing any work and answers `401` when it
does not verify. The installation the delivery names picks the team it
runs in, and GitHub's own record of which installation covers the
repository is read on every delivery that would start a run, so a
delivery runs only pipelines a team owner subscribed to a repository
that owner's installation covers. A pull request from a fork starts
nothing. Replay is closed by the body digest described above, which
covers the signed body rather than the `X-GitHub-Delivery` header the
sender picks.

## Secrets at rest

Configure a master key and secret values are encrypted with an
XChaCha20-Poly1305 AEAD cipher (`internal/secrets`), under a fresh random
nonce per value, before they reach the database. The key is 32 random
bytes, base64-encoded; generate one with `openssl rand -base64 32`.
Provide it via:

- `--secrets-key-file <path>` -- a file holding the raw or base64 key, or
- `SPARKWING_SECRETS_KEY` -- a base64-encoded 32-byte key.

Prefer the file. The chart mounts `controller.secretsKey` as a file and
renders the flag, because an environment entry is readable through
`/proc` and is inherited by anything the container execs. The controller
clears either variable from its own environment as soon as it reads it,
so a value supplied that way does not outlive startup.

A controller whose license allows more than one team refuses to start
without a key, because it holds other people's credentials. A
single-team install, a laptop controller included, still starts without
one: it stores secret values as plaintext and logs a warning at startup.
Nothing generates a key on its own; losing the key loses every value
sealed under it, so it is backed up beside the database (see
[backup-restore.md](backup-restore.md)).

Each envelope (`enc:v3:`) is bound, as additional authenticated data, to
the fields of the row that decide who may read it: the team that owns
the row, the secret name, the owning pipeline (empty for an unscoped
secret), whether an unscoped row is shared with every run, and whether
the value is masked in run output. Anyone with database write access who
copies a ciphertext into another team's row, onto another name, into
another pipeline or onto the unscoped row, or who edits a row to widen
its own access, gets a value that fails to open rather than one that
answers there. A read that fails to open answers `500`, never an empty
value.

Every start with a key reseals the table before the controller serves a
request. A row held as plaintext, because it was written while the
controller ran without a key, is sealed. An envelope from before team
binding (`enc:v1:`, bound to nothing, or `enc:v2:`, bound to the row but
not its team) is opened and resealed with its team. Those older envelopes
were only ever written into the `default` team, so one found in any other
team was copied there: it is left as it is, logged, and refused on read.
The pass walks the table in batches, writes a row only if it still holds
the value the pass read, and logs how many rows it resealed and skipped,
so a restart repeats nothing. Reads open only `enc:v3:` envelopes, so an
older envelope written into a row after the pass does not open either.

Before it writes anything, the pass opens a sample of the envelopes
already stored. If the key opens none of them, it is not the key the
table was sealed under, and the controller refuses to start instead of
sealing plaintext rows under it. It refuses the same way when the sample
holds envelopes but every one is an older envelope outside the `default`
team, which cannot confirm the key either way.

`sparkwing secrets list` reports `BOUND true` for a row sealed to its
team (`"bound"` on the API) and `false` for one that is plaintext or an
older envelope.

A stored envelope carries no key id, so the controller opens it by
trying the keys it holds. Name the key values were sealed under before
the current one and it becomes a read-only fallback:

- `--secrets-previous-key-file <path>`, or
- `SPARKWING_SECRETS_PREVIOUS_KEY`.

A value that does not open under the current key is tried against that
one, which keeps every value readable across a key change. Close the
window with `sparkwing secrets rotate --profile <name>`
(`POST /api/v1/secrets/rotate`, admin): it opens every row with the keys
the controller holds and writes it back sealed and bound to its team
under the current key, in one transaction, so the rotation lands for the
whole table or for none of it.

A row that opens under neither key keeps the bytes it had and is named
in the response, which is what a value written as plaintext before
encryption was enabled looks like when it happens to start with an
envelope prefix. The rest of the table still rotates. `sparkwing secrets
rotate` lists those rows; re-set each one, or name the key it was sealed
under, and rotate again. Drop the previous key once a rotation reports
nothing skipped.

Configure the keys in this order for a key change: mount the new key as
`secretsKey` and the outgoing one as `secretsPreviousKey`, restart,
rotate, then clear `secretsPreviousKey`. A controller started with a
previous key and no current key refuses to start, because it would have
nothing to seal new values under.

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

Container images follow the same rule. The release signs each image digest
with cosign, then moves `vX.Y.Z` onto it with
`docker buildx imagetools create`. It no longer scans the image first; the
CI/CD group is reintroducing that scan deliberately, with the rest of the
release-side checks. `bin/publish-image-tags.sh` resolves every
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
rather than repository content. A cache published outside the cluster starts
with `--disable-proxy`, which drops `/proxy/` and `/stats`, and
`--metrics-addr`, which moves `/metrics` to a listener the ingress does not
route to.

Registering a repository name validates it against
`^[A-Za-z0-9._-]{1,64}$`, and repointing a name that already maps to a
different repository requires the token even on an unauthenticated cache.
Every response carries `X-Content-Type-Options: nosniff`, and artifact
downloads are served as `application/octet-stream` attachments.

Off-cluster runners read Git through
`/api/v1/runs/<run>/gitcache/git/...`. That route requires `nodes.claim`, a
live claim on the named run, and the repository recorded on its trigger. The
unscoped `/api/v1/gitcache/git/...` route remains admin-only. The controller
drops the caller's bearer and presents its own cache credential upstream, and
permits only registration and upload-pack reads. A login-enabled dashboard
exposes those paths to machine bearers without accepting browser sessions:
the mount rejects a request carrying no bearer before it extends the half-hour
stream deadline or proxies anything, and caps concurrent Git streams. A
direct cache receives the run's cache grant instead, which opens only that
team's blob trees.

Within a team, a grant carries the repository and refs the controller read
from the run's trigger, and the cache writes `/cache`, `/bin` and `/artifacts`
entries only under the run's own ref. It reads that ref, the pull request's
base branch, the default branch, and then entries written before grants
carried a scope, in that order. A branch's run therefore cannot replace or
delete an entry its base branch's runs restore, and another repository's run
does not read it. A run's ref counts only when the server holds it: a signed
GitHub webhook, a schedule that follows the branch tip, or a retry or child of
one of those that keeps its ref and commit and uploads no source. A run whose
ref and commit its submitter chose, including a member with `runs.write`
naming `main` from the CLI, API or dashboard, writes under a ref of its own and
never over what `main`'s pushes restore. Entries a
run writes under its own ref are still restored by that ref's later runs, so
whoever can push a branch can poison that branch's cache.

Content-addressed artifact keys (`artifacts/blobs/<sha256>` and
`artifacts/manifests/<sha256>`) are the exception: they are shared by the
whole team, as the controller's direct uploads already store them, so a
memoized or retried node on another ref can stage what a feature ref
produced. The cache refuses an upload whose bytes do not hash to the key,
refuses a grant's delete of one, and every reader checks the digest again, so
a branch can add artifacts but cannot change the bytes another run stages.
Which manifest a node stages is decided by the controller's run and memo
records, not by the cache.

The runner-bundle chart ships a default-deny ingress NetworkPolicy for the
cache pod (`networkPolicy.enabled`, on by default). It admits the release's
runner, controller, and dashboard pods plus the Job pods the Kubernetes runner
backend creates (`app.kubernetes.io/name: sparkwing-runner`), and refuses to
render a non-`ClusterIP` cache Service unless a token Secret is configured. A
controller or runner pool outside the cluster reaches the cache through
`networkPolicy.extraIngress`, which is appended to the rule verbatim and takes
an `ipBlock` for the caller's source range.

`pipeline trigger --working-tree` refuses to upload a snapshot whose manifest
holds a secret-shaped file: by name, by a key or certificate block in any text
file, or by the first 64 KiB of a settings or manifest file, using the
credential vocabulary the detached-run environment filter uses. An operator sends such a file only by naming its path
with `--allow-secret-file`, so the audit of what left the laptop is the command
itself.

`pipeline trigger --working-tree` uploads one immutable source object per
submission. The controller binds it to one run and signs reads only for that
run's live claim. An unused upload expires 24 hours after commit; a bound
upload stays through a queued or running run and expires 24 hours after that
run finishes. A retry uploads a new bundle. Source bytes count against the
team's cache storage share. The hourly storage pass deletes the S3 object
before releasing its database row and quota.

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
does not refuse a connection on credentials.

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

## Runner Job placement

The Kubernetes runner keeps each team's Jobs on nodes of their own, because a
node also carries state that team code can reach, such as a Docker daemon. Every
Job and its pod carry the label `sparkwing.dev/team` with the run's team, and
every pod requires, on `kubernetes.io/hostname`, that no pod in any namespace
with that label and a different value runs on its node. Jobs of one team still
share nodes; a Job of another team waits for, or makes an autoscaler such as
Karpenter provision, a node with no other team's Job on it.

The label value is the team name when it is a DNS label (lowercase letters,
digits and inner hyphens, at most 63 characters) and does not start with
`sha256-`. Any other name becomes `sha256-` and the first 40 hex digits of the
SHA-256 of the name. A run whose trigger names no team is labeled `default`.

The runner that creates these Jobs runs the team's pipeline binary, so the
placement holds against a mistake, not against a pipeline that talks to the
Kubernetes API itself. Enforcing it against team code takes an admission policy
that refuses a runner pod without the label and the term.

## Verified self-update

`sparkwing update` proves the bytes it installs are the release's bytes
before and after it installs them. The release signs the `SHA256SUMS`
manifest with an ed25519 private key; the updater carries the matching
public key compiled into the binary and verifies the detached
`SHA256SUMS.sig` with pure-Go `crypto/ed25519` -- no external tool and no
network beyond fetching the asset, its detached signature, `SHA256SUMS`,
and `SHA256SUMS.sig`. It
then checks the download against the signed digest. A signature covers
bytes, not a tag, so it also runs the staged binary's
`version -o json --offline` and refuses one that reports a version other
than the requested release. It then installs atomically,
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
weekly. The release workflow calls none of it: a tag builds and publishes the
commit those runs already covered on `main`. The CI/CD group is reintroducing
the release-side scanners deliberately.

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

- **Set the auth tokens.** With an empty tokens table the controller
  serves every endpoint unauthenticated. It logs a warning at startup,
  reports `"auth": "disabled"` on `GET /api/v1/health`, and `sparkwing
  cluster status` flags the controller probe as a warning -- fine for a
  laptop, not for a shared deployment. Set `SPARKWING_REQUIRE_AUTH=1`
  (or `--require-auth`) so the pod refuses to start with an empty tokens
  table. A controller with a multi-team license never serves
  unauthenticated, whatever the tokens table holds. See [auth.md](auth.md).
- **Provision the first admin token.** Hand the controller the first
  admin credential and it never serves a request unauthenticated:
  `SPARKWING_BOOTSTRAP_ADMIN_TOKEN` carries the token itself, and
  `--bootstrap-admin-token-file <path>` reads it from a mounted file.
  When the tokens table is empty the controller stores that token's
  argon2 hash as an admin credential under the principal
  `bootstrap:admin` before it binds the listener, which satisfies
  `--require-auth` on a first start. A table that already holds a token
  is left alone, so restarting with the same secret mounted neither
  duplicates the row nor revives a revoked one. The value has to look
  like a minted token -- `swu_` followed by at least 28 characters, for
  example `printf 'swu_%s' "$(openssl rand -hex 24)"` -- because a
  bearer lookup selects on that prefix. The chart mounts it from
  `controller.bootstrapAdminToken.name` as a file, renders the flag, and
  renders `--require-auth` from `controller.requireAuth`, which defaults
  to `true`. Without a bootstrap token the render refuses unless
  `controller.allowOpenBootstrap=true`, which drops `--require-auth` so
  the first token can be minted through the open window and auth enabled
  by a restart. Anything that reaches the Service during that window can
  mint an admin token, so keep it closed to everything but the operator.
- **Know what the bootstrap flag treats as an empty table.** It writes
  when no token *authenticates*: every row is revoked, expired, or the
  table is empty. That is what recovers a cluster whose only credential
  was revoked or ran out, and it is also why revoking the bootstrap
  token while its Secret stays mounted recreates the same credential on
  the next restart. Unmount the Secret (clear
  `controller.bootstrapAdminToken.name`) before revoking, or mint a
  replacement admin token first so the table still holds a live one.
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
  | `--max-inflight-bytes` (`SPARKWING_LOGS_MAX_INFLIGHT_BYTES`) | 32MiB | Request-body bytes all in-flight appends may hold in memory at once; further appends are refused with `503`. Keep it well under the pod's memory limit. |
  | `--min-free-bytes` (`SPARKWING_LOGS_MIN_FREE_BYTES`) | 512MiB | Free space on the volume below which appends are rejected with `507`, leaving room to read and delete what is already stored. A volume the service cannot measure is treated as full. |
  | `--retention` (`SPARKWING_LOGS_RETENTION`) | 0 (off); 2160h with `--archive-store` | Age after a run's last write at which the sweeper deletes its logs. Off by default without an archive so an upgrade deletes nothing; with one, 90 days unless the operator names another value, zero included. A run of a team without credits keeps its archived logs 30 days at most. |
  | `--sweep-interval` (`SPARKWING_LOGS_SWEEP_INTERVAL`) | 1h | How often the sweeper runs. |
  | `--search-max-bytes` (`SPARKWING_LOGS_SEARCH_MAX_BYTES`) | 256MiB | Bytes one `GET /api/v1/logs/search` may read. |
  | `--search-timeout` (`SPARKWING_LOGS_SEARCH_TIMEOUT`) | 10s | How long one search may scan. |
  | `--max-line-bytes` (`SPARKWING_LOGS_MAX_LINE_BYTES`) | 0 (off) | Byte cap for one log line, marker included: every line past it is stored cut to the cap with a `[sparkwing-logs] truncated: line byte cap reached` marker in place of its tail. The cut lands on a UTF-8 rune boundary. A cap too small to hold the marker and a byte of output is refused at startup, and raised to that minimum when set through the Go API. |
  | `--binary-ratio` (`SPARKWING_LOGS_BINARY_RATIO`) | 0 (off) | Share of bytes in one append that read as binary rather than text, above which the append is dropped and one `[sparkwing-logs] dropped` line is stored for that node log. Control bytes count, and so does any byte above `0x7f` that is not part of a valid UTF-8 sequence, which is what catches a gzip or tar blob while leaving text in any language stored as sent; `0.3` is a workable threshold. |
  | `--max-store-bytes` (`SPARKWING_LOGS_MAX_STORE_BYTES`) | 0 (off) | Stored bytes across the whole log store, not one node or run. At or above it every append is refused with `507` naming the ceiling, until a measurement finds the store back under it. |
  | `--max-store-objects` (`SPARKWING_LOGS_MAX_STORE_OBJECTS`) | 0 (off) | Same ceiling counted in log files. |
  | `--warn-store-bytes` (`SPARKWING_LOGS_WARN_STORE_BYTES`) | 0 (off) | Stored bytes at which `/api/v1/health` reports the store as warning, refusing nothing. |
  | `--warn-store-objects` (`SPARKWING_LOGS_WARN_STORE_OBJECTS`) | 0 (off) | Same warning counted in log files. |
  | `--store-reconcile` (`SPARKWING_LOGS_STORE_RECONCILE`) | 1h | How often the service walks the store and replaces its running count with the measurement. `0` measures once at startup. Deleting a run measures it again straight away. |

  A search that hits either budget, or whose caller disconnects, returns
  the matches it found with `"truncated": true` and a `reason` naming the
  limit. Archived runs are scanned from their object-store logs without
  restoring the run to the local volume. The archive's recorded team is
  checked before reading a log object. Search also requires
  `run_id`; a query without one is refused with `400` rather than
  walking every stored run.

  The runner-bundle chart passes these through as `logs.limits.*`
  (`maxNodeBytes`, `maxRunBytes`, `maxInflightBytes`, `minFreeBytes`,
  `retention`, `sweepInterval`, `searchMaxBytes`, `searchTimeout`,
  `maxLineBytes`, `binaryRatio`); an
  empty value keeps the binary's default. Size them against
  `logs.storage.size`, because a volume left to fill answers `507` to
  every append until you turn on retention or delete runs. A malformed
  or negative value stops the service at startup rather than falling
  back to the default.

- **Terminate TLS at your ingress.** Sparkwing speaks plain HTTP; put it
  behind an ingress/proxy that enforces HTTPS.
- **Pin image digests** rather than floating tags. Each release lists them
  in its `image-digests.json` asset.
- **Encrypt etcd / your secret store.** Kubernetes Secrets are
  base64, not encrypted, unless the cluster enables it.
- **Rotate the GitHub credentials and cache SSH key** periodically.

## Controller client redirects

Controller API clients attach their bearer to the initial request. Go's default HTTP
redirect handling strips that header on redirects to unrelated hosts. A caller
can supply a different CheckRedirect policy. Signed artifact URLs carry their
own grant and receive no controller bearer, including on the controller origin.
