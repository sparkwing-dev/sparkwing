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
ESLint, and `npm run build` for the production static export.

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

## Learn more

Next.js itself is documented at <https://nextjs.org/docs>; this repo's
conventions for it are in `web/AGENTS.md`.
