# Migrating to the next release

## Node logs reach the controller as segments

- **Before:** a Kubernetes Job, warm runner, self-hosted agent, GitHub
  Actions runner, launcher Job or the trigger runner's in-process nodes
  appended node logs to the logs service at their logs URL, as did a laptop
  run against a remote profile, and without one
  wrote them to a disk nobody could read and that went away with the pod.
  `sparkwing runs logs --profile` and `runs logs --grep --profile` read the
  logs service the profile or the controller named.
- **After:** against a controller that keeps segments, every one of those
  runners, and `sparkwing run` on a laptop whose profile keeps the run's
  state on that controller, uploads each node's log as gzip segments plus a sealing manifest
  through URLs the controller issues, into the controller's cache bucket
  (`--cache-blob-store`) or, without one, its output directory, whether or
  not it has a logs URL. The dashboard and the CLI read every node through
  the controller, which serves segments first and the logs service for a
  node that wrote none, so logs the service already holds stay readable.
  Deleting a run or a team deletes its segments.
- **Operator steps:** keep the logs service running so its existing logs
  stay readable. On an S3 cache bucket, grant the controller
  `s3:GetLifecycleConfiguration` and add enabled lifecycle rules expiring
  `<cache prefix>/logs/r90/` after 90 days and `<cache prefix>/logs/r30/`
  after 30, plus `<cache prefix>/logs/r<days>/` after `<days>` for each
  team `--log-retention team=days` names; [self-hosting.md](../self-hosting.md#node-log-retention)
  has the command. Until a class has its rule, the controller issues no
  upload URLs into it and those nodes' runners keep writing to their logs
  URL, or their own disk without one. A controller that keeps segments on
  its own disk needs no rule and keeps them until their run is deleted.
  On a multi-team controller segment bytes count against the team's log
  storage.
- **Logs the service already holds:** run `sparkwing-controller migrate-logs`
  once against the service's root and archive and the controller's cache
  bucket and state database (see [self-hosting.md](../self-hosting.md#moving-the-logs-services-logs-to-segments))
  before you retire the service; logs not converted become unreadable.
- **Mixed versions:** upgrade the controller first. A runner that predates
  segments keeps writing to its logs URL, which the controller still reads
  through. A runner of this release against an older controller finds no
  segments advertised and writes to its logs URL, or locally without one,
  as before. Rolling the runners back returns their new logs to the logs
  service.

## The logs service is removed

This release ships no `sparkwing-logs`. Its steps replace the ones in
[Node logs reach the controller as segments](#node-logs-reach-the-controller-as-segments)
that keep the logs service running.

- **Before:** `sparkwing-logs` held node logs that a runner without
  segments, or a node from before segments, wrote; the controller read
  through it and announced it as `logs` in `GET /api/v1/services`. The
  runner-bundle chart deployed it, and runners, agents and profiles named
  its URL.
- **After:** the controller is the only place node logs go. It keeps them
  as segments in its cache bucket (`--cache-blob-store`) or, without one,
  its output directory; a controller with neither keeps no node logs, so a
  runner keeps the log on its own disk and a laptop run against it fails
  at start naming the controller. Pipeline binaries built against any SDK
  keep logging unchanged: the runner serves their appends on loopback.
- **Before you upgrade:** scale the logs service and the controller to
  zero and run this release's `sparkwing-controller migrate-logs` once,
  `--dry-run` first, as a Job that mounts the logs PVC and the controller's
  data volume with the controller's credentials, as
  [self-hosting.md](../self-hosting.md#moving-the-logs-services-logs-to-segments)
  describes in order. It reads the service's volume and archive directly,
  records each converted log in the controller's state database, skips
  what it already converted, and can run again. It names and leaves
  unconverted any run it cannot convert whole, such as one the controller's
  database does not hold. Logs not converted become unreadable.
- **Helm:** delete `sparkwing-runner-bundle.logs` (or `logs` for a
  standalone runner bundle) and `controller.logs` from your values; a
  values file that still sets them fails to render. The upgrade removes the
  logs Deployment, Service and ServiceAccount; the logs PVC is kept by its
  resource policy, so delete it once the export is done:

  ```sh
  kubectl -n <namespace> delete pvc <release>-sparkwing-runner-bundle-logs
  ```

- **Controller:** drop `--logs-url` and `--logs-archive-store`, and delete
  the `logs-delete-token` file from `--credentials-dir` (or the
  `controller.credentialsSecret` key); nothing reads it. The storage pass
  counts and prunes logs from the segments, and deleting a team deletes its
  segments.
- **Runners and agents:** drop `--logs` from `sparkwing-runner runner` and
  `launch`, `--trigger-runner-logs-url` from `runner`, and the `logs:` key
  from an agent's `config.yaml`, which now fails to load with
  `field logs not found`. `sparkwing cluster runners add` has no `--logs`.
  Drop any `SPARKWING_LOGS_EGRESS_*` variables; nothing reads them.
- **Profiles:** a `logs: {type: controller}` surface takes no `url`; remove
  it. That surface now needs the profile's `state:` on the same
  controller, because the controller keeps only the logs of runs it holds;
  a profile that keeps state locally and sent logs to a logs service
  chooses a `filesystem` or `s3` logs surface instead.
- **Credentials and API:** the `logs.write` and `logs.delete` scopes are
  gone, and minting a token with either is refused; runner tokens carry
  `nodes.claim`, `triggers.claim`, `runs.state` and `secrets.read`.
  Existing tokens that list them keep working with the rest of their
  scopes. `GET /api/v1/runs/{id}/log-access`,
  `POST /api/v1/runs/{id}/nodes/{nodeID}/claim/validate`, the `logs` field
  of `GET /api/v1/services` and signed `kind=log` downloads are removed;
  read logs through `GET /api/v1/runs/{id}/logs/{node}`. The storage
  counter routes answer only the cache's operator token.
- **Laptop:** `sparkwing serve` runs no logs service and writes only
  `SPARKWING_CONTROLLER_URL` to `dev.env`; the dashboard reads node logs
  from the run directories, and `sparkwing runs prune` removes them through
  the dashboard.
- **Go packages:** `pkg/logs` and `pkg/storage/sparkwinglogs` are removed.
  Read node logs with `client.NodeLogs` from
  `pkg/controller/client`, and their completeness with `pkg/storage/logseg`.
- **Mixed versions:** upgrade the runners and the controller together. A
  runner from before segments has nowhere to send its logs once the
  service is gone, and keeps them on its own disk.

## Operator powers need the operator console

- **Before:** an admin token reached a team's billing standing and the
  sign-up waitlist through `GET`/`POST /api/v1/teams/{team}/trust`,
  `GET /api/v1/signups/waitlist`, `POST /api/v1/signups/waitlist/approve`,
  `free` and `reversal` grants on `POST /api/v1/credits/grants`, holds
  naming a team and every release on `POST /api/v1/credits/freezes`, and
  refunds on `POST /api/v1/credits/reversals`. It also passed every other
  `credits.grant` route.
- **After:** those powers answer only a listed operator's own signed-in
  console session (`--operator-accounts`). The four admin routes above are
  removed. `POST /api/v1/credits/grants` records `paid` grants only and
  `POST /api/v1/credits/freezes` holds by `payment_id` and `dispute_id`
  only; the `reverses`, `team` and `release` fields are gone and are
  refused as unknown. Every `credits.grant` route answers `403` to an
  admin token. The console's trust action sets the daily cap, card rung and
  Cloud node cap alongside the purchase limit, and its unfreeze releases a
  payment dispute's hold named by `hold`. The new
  `sparkwing-controller admin freeze|unfreeze` holds or releases a team
  from inside the controller pod when console sign-in is down.
- **Migrate:** list the operators' account ids in `--operator-accounts`
  (Helm: `controller.operatorAccounts`) and have each sign in with Google
  or GitHub before the upgrade; a password session does not reach the
  console. Move each call:

  | Before (admin token) | After |
  |---|---|
  | `GET /api/v1/teams/{team}/trust` | `GET /api/v1/operator/teams/{team}`, field `billing` |
  | `POST /api/v1/teams/{team}/trust` | `POST /api/v1/operator/teams/{team}/trust`, same body |
  | `GET /api/v1/signups/waitlist` | `GET /api/v1/operator/waitlist`, newest first, with `offset` |
  | `POST /api/v1/signups/waitlist/approve` | `POST /api/v1/operator/waitlist/approve`, same body |
  | `free` grant on `POST /api/v1/credits/grants` | `POST /api/v1/operator/teams/{team}/grants` with `amount_cents`, `reason` and `key` |
  | `reversal` grant, or a refund on `POST /api/v1/credits/reversals` | refund in the Stripe dashboard; the checkout service reverses it |
  | hold naming `team` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/freeze` |
  | `release` on `POST /api/v1/credits/freezes` | `POST /api/v1/operator/teams/{team}/unfreeze`, with `hold` for one dispute's hold |

  The checkout service's own `credits.grant` token keeps working
  unchanged. A script that used the admin token for any row above now
  gets `404` or `403`.
- **Go:** pass the actor and reason to `Store.ReleaseCreditFreezes`; a
  caller of the removed `Store.DisputeTeam` reads the team from the hold it
  holds, or from `Tenant.CreditFreeze`.
