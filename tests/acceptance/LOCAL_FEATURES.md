# Local controller feature scenarios

These Linux scenarios drive an independently built CLI, its controller and runner,
the embedded dashboard, and real shell descendants. They never use the caller's
Sparkwing state. Each scenario creates a private HOME, config, key file, repository,
state store and random dashboard port. The Go build and package limits are two.

Build the revision to verify with its dashboard assets, then pass the absolute
executable path. For an Xwing candidate, retain `xwing tool list --session <n>`
and the launcher's `version -o json` output as the build provenance. The scenario
processes use that candidate's immutable executable in fresh homes because the
candidate launcher binds all invocations to one managed home and does not support
detached runs. The outer pipeline can run through the candidate launcher.

```sh
env -C /absolute/checkout GOWORK=off GOMAXPROCS=2 GOFLAGS=-p=2 \
  sparkwing run local-controller-e2e -- --binary /absolute/built/sparkwing
```

Install `web/` dependencies and the matching Playwright Chromium first:

```sh
pnpm --dir /absolute/checkout/web install --frozen-lockfile --ignore-scripts
pnpm --dir /absolute/checkout/web exec playwright install chromium
```

`PLAYWRIGHT_BROWSERS_PATH` may select an existing browser cache. Otherwise the
test uses the caller's cache before isolating HOME. Missing browsers fail the
scenario. The executable must contain the dashboard bundle. The Xwing installer
builds it through the repository's normal installer.

For a grid cell or a focused rerun, use the same executable and this command:

```sh
env GOWORK=off GOMAXPROCS=2 SPARKWING_LOCAL_E2E_BINARY=/absolute/built/sparkwing \
  go -C /absolute/checkout test -p 2 -count=1 -timeout=20m -v \
  -run '^TestLocalControllerFeatureE2E$' ./tests/acceptance
```

Append `/dashboard`, `/lifecycle`, `/host-exec`, or `/schema-backup` to the test
filter to select one feature. Unselected opt-in tests skip when the binary
variable is absent. A configured binary that cannot run fails. Retain Go's JSON
output (`-json`) or the pipeline run logs for each revision and platform cell.
There are no production services, cloud credentials or credits in this path.

The assertions cover:

- Dashboard link exchange in headless Chromium, token storage without a cookie,
  one-use codes, another port in the same browser context, a second browser
  context, controller restart revocation, and logout through the UI.
- A live command and an owner awaiting a live child across `daemon restart
  --force`, followed by successful completion and the child's returned value.
  `daemon stop` must retire the runner before losing its controller connection.
- More than 200 KiB on each output stream with a secret crossing 64 KiB,
  cancellation of a child and grandchild while a second run holds a background
  process, and that background process surviving its command but ending with
  its SDK session.
- A schema 34 store upgraded by controller startup, a readable pre-upgrade
  backup with the old schema, preserved configured ciphertext, SQLite integrity,
  private backup permissions, and retention of the newest two backups.

File markers and process birth identities coordinate actors. Polls observe real
processes and files under a deadline. They do not assume a command has started
after an arbitrary delay. The harness releases held work and stops its controller
on failure. A remaining recorded child is killed and reported as a cleanup failure.

The schema fixture models an actual supported baseline by removing the Actions
tables and setting the recorded schema 34 digest. When the supported upgrade
floor changes, update this fixture with the matching schema rather than only
changing its version number. These scenarios cover local Linux execution.
Remote controllers, systemd boot installation and other operating systems need
separate cells.
