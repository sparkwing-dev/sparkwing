The sparkwing dashboard: a [Next.js](https://nextjs.org) SPA that the Go
binaries embed and serve.

## Getting started

Run `bash bin/dev-start.sh` from the repo root. It starts the dashboard
backend on :4343 (`sparkwing dashboard start`, serving /api/v1/* off
~/.sparkwing/state.db) and `next dev` on :3100. The dev-only rewrite in
`next.config.ts` proxies /api/* to the backend, so UI edits hot-reload
without rebuilding the Go binary.

Open <http://localhost:3100>. Stop both halves with `bash bin/dev-stop.sh`;
after a Go change, `bash bin/install.sh && bash bin/dev-restart.sh`.

Pages live under `src/app/` -- the dashboard home is `src/app/page.tsx`,
with sibling routes for runs, queue, cluster, analytics and the docs guide.
Shared UI is in `src/components/`. Edits hot-reload.

Run `npm test` for the dashboard's TypeScript unit suite, `npm run lint` for
ESLint, and `npm run build` for the production static export. Run `npm run
test:browser:install` once to cache Chromium, then `npm run test:browser` to
rebuild the static export and exercise the dashboard smoke suite. Failed
browser runs retain their trace, screenshot, video, and HTML report under
`test-results/` and `playwright-report/`; the hosted pre-commit gate uploads
those directories for 14 days when the browser suite fails. The gate clears
both directories before it starts and after it passes, and ESLint ignores
them. The suite runs
deterministic API fixtures against OS-assigned loopback ports; it needs no
controller, hosted secret, or Kubernetes cluster.

`sparkwing run pre-commit` runs the unit and full ESLint suites in parallel,
then the production build and browser smoke suite. Install the locked dashboard
dependencies before running the local gate; hosted CI runs `npm ci` itself.

## How this ships

`next build` static-exports the dashboard to `web/out/`. `bash
bin/build-web.sh` copies that into `internal/web/next-out/`, which
`cmd/sparkwing` (`sparkwing dashboard start`) and `cmd/sparkwing-web` (the
cluster dashboard pod) embed with `//go:embed all:next-out`.
`bin/install.sh` and the release workflow both run that script, so every
install and released artifact ships the current dashboard. Static export has
no request lifecycle, so runtime config (API token, controller URL) is
injected by the Go server via HTML templating. Set `SKIP_WEB_BUILD=1` on
`bin/install.sh` to reuse the existing bundle when iterating on Go code only.

When controller-backed login is active, browser requests stay same-origin and
authenticate with the session cookie, and the shared navigation shows `Log out`
at its right edge. The service bearer stays server-side; only the dashboard
proxy adds it to controller requests. Sessionless local dashboards retain their
existing runtime-token behavior. Login, bootstrap, and logout forms require a
same-origin CSRF token. Unsafe `/api/v1/*` requests also send the session CSRF
token in `X-CSRF-Token`; the Go proxy strips browser cookies and that header
before adding its service bearer upstream. HTML, data, and API requests
revalidate the controller session, so logout or controller-side revocation
takes effect on the next protected data request. Immutable `/_next/static/`
assets do not resolve a session.

`sparkwing-web --require-login` refuses to start without `--controller URL` or
a selected profile that declares `controller.url`. A state-only backend does
not provide browser sessions by itself. Login cookies are `Secure`; use HTTPS,
or set `SPARKWING_WEB_INSECURE_COOKIES=1` for a loopback-only local development
process. On a non-loopback bind that variable also needs
`--allow-insecure-cookies-remote`, which says the operator accepts session
cookies travelling without TLS.

## Learn more

Next.js itself is documented at <https://nextjs.org/docs>; this repo's
conventions for it are in `web/AGENTS.md`.
