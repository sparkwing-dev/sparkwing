# Sparkwing console onboarding, 2026-11-04

Evidence is staged in this worktree because the harness permits writes only to
this worktree and `/tmp/sw-codex-onboarding`. The requested final destination is
`/home/bombuntu/code/ink-labs/wing/sweeps/sparkwing-2026-11-04/speed-data/onboarding/`.
The lead needs to copy this directory there under the harness write restriction.
Do not discard the staged evidence before that copy is verified.

## Changes against the observed friction

| Friction | Change |
| --- | --- |
| 1. Display name mistaken for team slug | Team headers label the permanent slug, explain its external uses, and offer Copy. Repository setup and CLI commands use the active team's actual slug. |
| 2. GitHub redirects and repository selection split across sites | One repository setup entry point. The connect screen explains the round trip and distinguishes code access on GitHub from enabling runs in Sparkwing. Existing-installation linking stays under an expander. Connected account management moves behind a summary. OAuth and installation authorization are unchanged. |
| 3. Guided setup versus manual subscriptions | Guided setup is the main content. Advanced manual subscriptions explicitly describe their override purpose. Overrides appear in the trigger preview and prevent a misleading ready claim. |
| 4. Two unexplained permission buttons | One owner action, Enable runs on GitHub Actions, explicitly grants repository Actions execution and turns on automatic runs. Separate permissions and other run locations remain under details. Selection alone never grants anything. A partial failure rereads saved state and a retry performs only the missing action. |
| 5. Existing workflow offered another workflow | Existing workflows can be kept. Starter copy/download sits under Need a workflow. No new workflow is required when the existing one starts the correct runner. |
| 6. No end-to-end status | Progress shows GitHub connection, push triggers, saved permissions and customer-confirmed GitHub setup. Ready names the pipeline, branch filter and GitHub Actions, then links to Runs. Workflow and variables are explicitly customer-confirmed, not API-verified. Confirmations reset when switching repositories and do not survive reload. |
| 7. Expired CLI token, hidden replacement path | Machines begins with sparkwing login for this controller and team, explains browser approval, profile selection and expired-token replacement, and links to device-code entry. It gives the command to check access and explains how to renew another profile. Manual tokens remain for scripts. Repository setup links to this optional step. |
| 8. Jargon, dense buttons, no progress, mobile crowding | Plain-language labels, a four-step progress row, two-column desktop trigger/action layout, fewer bordered sections, advanced controls behind details, scrollable team tabs, and 44px primary/copy targets. The shared primary button now uses separate accessible background/hover tokens. |

The starter workflow embeds `--team` directly. `SPARKWING_TEAM` is required only
when the customer's workflow reads that variable. Setup explains both cases,
provides the exact slug, and links to repository variables and Actions.

## Click counts

Scenario: start on Create a team, with a pipeline already committed whose push
trigger names main. Connect a GitHub account through the normal install path,
choose a repository, and enable GitHub Actions execution. Browser sign-in is
already complete. Typing, scrolling, native picker mechanics and GitHub-owned
screens are excluded from control-activation counts below. These are console
counts from the exercised path, not claims about real GitHub interaction time.

| Console control activated | Before | After |
| --- | ---: | ---: |
| Create team | 1 | 1 |
| Navigate from Members to GitHub | 1 | 0 |
| Connect GitHub | 1 | 1 |
| Choose repository | 1 | 1 |
| Choose GitHub Actions as the run location | 1 | 0 |
| Enable automatic runs and allow Actions | 2 | 1 |
| Confirm existing enabled workflow and correct team | 0 | 1 |
| Total with existing workflow | 7 | 5 |
| Extra controls for a new workflow: Copy (and expand starter after) | 1 | 2 |
| Total with new workflow | 8 | 7 |

Counting each native picker as open + choose adds two physical clicks before and
one after: existing workflow **9 -> 6**, new workflow **10 -> 8**. The old flow
needs a separate run-location selection. Before had no completion
confirmation. After spends one activation on that confirmation. The minimum
permission/setup path before confirmation drops from seven activations to four.
A new workflow trades that saved navigation for clear review and confirmation.
Copying the slug adds one activation if needed. Before, selecting/copying text was
manual and the page did not identify which text external configuration needed.

Actual GitHub install/authorization screens, saving repository variables,
committing/enabling workflows, and pushing a commit are **not measured**. Real
GitHub calls are forbidden for this lane. The fixtures prove the console's
state transitions and request bodies, not hosted Actions execution or AWS access.
The console cannot automatically verify workflow installation with its existing
API, so no backend inspection service was added.

## Screenshots

Before: `28b950c89`, the starting tree. After: `6c99ae1b4`, including
`69cde2c1d` (team and CLI access), `6bd5b8f73` (guided repository setup), and
`6c99ae1b4` (documentation).
Desktop is 1440x1100. Mobile is 390x844. Files retain the viewport and step in
their names. Long console content scrolls within its main pane, so workflow and
ready shots capture the relevant scrolled region rather than an artificial
full-height layout.

| Step | Before | After |
| --- | --- | --- |
| Server-rendered sign-in | 01-sign-in | 01-sign-in |
| Create team | 02-create-team | 02-create-team |
| Team identity and members | 03-team | 03-team |
| Connect GitHub | 04-connect-github | 04-connect-github |
| Repository and triggers | 05-repository | 05-repository |
| Actions permission and workflow | 06-workflow | 06-workflow |
| Ready | No equivalent state | 07-ready |
| CLI/machines | 08-cli-machines | 08-cli-machines |
| Device code | 09-device | 09-device |
| Device approval | 10-device-approval | 10-device-approval |

The captures contain 18 before and 20 after PNGs. The after captures also preserve raw axe
JSON for each state. No axe rules are disabled. `capture.ts.txt` retains the
capture script. Copy it to a temporary `.ts` path before running with tsx.
`baseline-capture.ts.txt` and `baseline-server.ts.txt` retain the baseline capture
and server scripts. The baseline capture needs the static export built from
`28b950c89` at `/tmp/sw-codex-onboarding/baseline/web/out`. The after script needs
this lane's build. API fixtures are in
`web/tests/browser/onboarding-fixture.ts`.
`initial-captures/` retains the earlier screenshots taken before the fixture's
unset run location was corrected to match the controller response.

## Runtime isolation and verification

The local Go controller fixture serves the actual login and authorization routes.
Its GitHub provider is a loopback httptest server. Exported console pages use the
existing Playwright static server and deterministic API routes. Capture blocks
browser requests outside loopback. HOME, SPARKWING_HOME and TMPDIR point under
`/tmp/sw-codex-onboarding`; GOPATH, GOMODCACHE and ASDF_DATA_DIR retain the requested
shared toolchain locations. Go uses GOWORK=off, GOFLAGS=-buildvcs=false and the
requested /tmp/sw-gocache. No cluster, cloud, payment provider or real GitHub was
used.

- Unit suite: 525 tests passed.
- ESLint and production static export passed.
- Full browser run: 144 passed, one stale home-link expectation failed. That
  expectation was updated to assert the new setup and CLI entry links.
- Follow-up browser run: 97 passed, including dashboard, authenticated dashboard,
  GitHub onboarding, callbacks, login axe and keyboard/touch explanations.
- Resumed full browser suite: 147 passed. It includes saved-permission/reload
  coverage, unsupported configuration, and exact permission request bodies.
- The isolated home's first browser rerun could not build the fixture because
  asdf had no Go version selected. Selecting the installed Go version fixed the
  environment. Both the failure and successful rerun logs are retained.
- The final unset-run-location correction has its own production build, focused
  browser run, lint, and refreshed screenshots. See `logs/*final*`.
- CLI profile guidance has a further production build, 14-test onboarding run,
  lint, and refreshed after screenshots. See `logs/*profile-guidance*`.
- All 20 final screenshot axe reports contain zero violations.
- Docs mirror, changelog lint and whitespace checks passed.
- The lead owns the full repository gate. No Go/controller behavior changed.
- Performance latency, LCP, INP, hosted workflow execution and live provider
  settings were not measured. No inferred performance numbers are presented.

No release, landing or push was performed. The evidence directory is intentionally
untracked so the lead can move it to the requested sweep archive without adding
PNG and axe output to the product repository.
`SHA256SUMS` inventories the retained files for verification after the archive copy.

## Reproduce the final checks and captures

From this worktree, keep the shared toolchain locations while isolating runtime
state. `ASDF_GOLANG_VERSION` selects an installed compiler. Go then resolves the
module's toolchain directive.

```sh
export HOME=/tmp/sw-codex-onboarding/home
export SPARKWING_HOME=/tmp/sw-codex-onboarding/state
export TMPDIR=/tmp/sw-codex-onboarding
export GOPATH=/home/bombuntu/.go
export GOMODCACHE=/home/bombuntu/.go/pkg/mod
export ASDF_DATA_DIR=/home/bombuntu/.asdf
export ASDF_GOLANG_VERSION=1.26.9
export GOWORK=off
export GOFLAGS=-buildvcs=false
export GOCACHE=/tmp/sw-gocache
export PLAYWRIGHT_BROWSERS_PATH=/home/bombuntu/.cache/ms-playwright
mkdir -p "$HOME" "$SPARKWING_HOME"
pnpm --dir /var/tmp/sw-lead-tmp/onboarding-wt/web test
pnpm --dir /var/tmp/sw-lead-tmp/onboarding-wt/web run lint
pnpm --dir /var/tmp/sw-lead-tmp/onboarding-wt/web run build
pnpm --dir /var/tmp/sw-lead-tmp/onboarding-wt/web exec playwright test
go -C /var/tmp/sw-lead-tmp/onboarding-wt build -o /tmp/sw-codex-onboarding/browser-fixture ./internal/web/browserfixture
export AUTHENTICATED_DASHBOARD_FIXTURE=/tmp/sw-codex-onboarding/browser-fixture
cp /var/tmp/sw-lead-tmp/onboarding-wt/web/onboarding-evidence/capture.ts.txt /tmp/sw-codex-onboarding/capture.ts
pnpm --dir /var/tmp/sw-lead-tmp/onboarding-wt/web exec tsx /tmp/sw-codex-onboarding/capture.ts after
```

The capture script refuses external browser requests. The controller fixture's
GitHub provider is a local test server. Rebuilding the baseline export is only
needed to reproduce before screenshots, not to run the product tests.
