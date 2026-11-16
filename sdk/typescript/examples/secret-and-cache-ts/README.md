# secret-and-cache-ts

A TypeScript pipeline that tests with a cached npm install, then deploys with a
declared secret. `secrets: [...]` names `DEPLOY_TOKEN` before any job runs, so a
run without it fails at the start with the command that sets it. `ctx.secret`
reads the value inside the job, and the host masks it in every log line. The
deploy job retries twice with a ten-second backoff, and both jobs have timeouts.

To use it, scaffold with `sparkwing pipeline new --pipeline release --template typescript`,
replace `.sparkwing/jobs/release.ts` with this file, add `scripts/deploy.sh`,
store the token with `sparkwing secrets set --secret DEPLOY_TOKEN --file <path>`,
and run `sparkwing run release`.
