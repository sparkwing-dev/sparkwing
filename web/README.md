The sparkwing serve: a [Next.js](https://nextjs.org) SPA that the Go
binaries embed and serve.

## Getting started

Run `bash bin/dev-start.sh` from the repo root. It starts the dashboard
backend on :4343 (`sparkwing serve start`, serving /api/v1/* off
~/.sparkwing/state.db) and `next dev` on :3100. The dev-only rewrite in
`next.config.ts` proxies /api/* to the backend, so UI edits hot-reload
without rebuilding the Go binary.

Open <http://localhost:3100>. Stop both halves with `bash bin/dev-stop.sh`;
after a Go change, `bash bin/install.sh && bash bin/dev-restart.sh`.

Pages live under `src/app/` -- the dashboard home is `src/app/page.tsx`,
with sibling routes for runs, queue, cluster, analytics and the docs guide.
Shared UI is in `src/components/`. Edits hot-reload.
Home shows a short setup guide while fewer than five runs are in its overview
window. Its empty attention state links to Runs.
On the runs Activity view, closing a detail pane keeps the run list compact
until the pane finishes expanding, so row heights stay steady.

Run `pnpm test` for the dashboard's TypeScript unit suite, `pnpm run lint` for
ESLint, and `pnpm run build` for the production static export. Run `pnpm run
test:browser:install` once to cache Chromium, then `pnpm run test:browser` to
rebuild the static export and exercise the dashboard smoke suite. Failed
browser runs retain their trace, screenshot, video, and HTML report under
`test-results/` and `playwright-report/`; the hosted pre-commit gate uploads
those directories for 14 days when the browser suite fails. The gate clears
both directories before it starts and after it passes, and ESLint ignores
them. The suite runs
deterministic API fixtures against OS-assigned loopback ports; it needs no
controller, hosted secret, or Kubernetes cluster.

`sparkwing run gate` runs the unit and full ESLint suites in parallel,
then the production build and browser smoke suite. Install the locked dashboard
dependencies before running the local gate; hosted CI runs `pnpm install
--frozen-lockfile` itself.

## How this ships

`next build` static-exports the dashboard to `web/out/`. `bash
bin/build-web.sh` copies that into `internal/web/next-out/`, which
`cmd/sparkwing` (`sparkwing serve start`) and `cmd/sparkwing-controller`
(the cluster controller) embed with `//go:embed all:next-out`.
`bin/install.sh` and the release workflow both run that script, so every
install and released artifact ships the current dashboard. A controller built
without that step still starts, and its dashboard pages answer 503 naming the
build step. Static export has no request lifecycle, so the page reads its
runtime config (version and login mode) from `/sparkwing-runtime.js`. Set
`SKIP_WEB_BUILD=1` on `bin/install.sh` to reuse the existing bundle when
iterating on Go code only.

When the controller requires sign-in, browser requests stay same-origin and
authenticate with the session cookie, and the shared navigation shows `Log out`
at its right edge. `sparkwing serve` dashboards sign in with the serve token
instead. Login, bootstrap, and logout forms require a same-origin CSRF token.
Unsafe `/api/v1/*` requests also send the session CSRF token in
`X-CSRF-Token`, which the controller checks against the cookie and the live
session. Page and API requests revalidate the session, so logout or
controller-side revocation takes effect on the next request. Immutable build
assets do not resolve a session.

Session cookies are `Secure`; reach the controller over HTTPS, or pass the
controller `--insecure-cookies` for a dashboard published over plain HTTP,
which accepts session cookies travelling without TLS.

## Learn more

Next.js itself is documented at <https://nextjs.org/docs>; this repo's
conventions for it are in `web/AGENTS.md`.
