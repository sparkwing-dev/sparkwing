# lint-test-node-ts

A TypeScript CI check for a Node project: one job installs dependencies once,
then runs `npm run lint` and `npm test` as parallel steps. Steps share the job's
workspace, so both read the same `node_modules`, and `npm()` restores npm's
download cache between runs.

To use it, scaffold a TypeScript project with
`sparkwing pipeline new --pipeline check --template typescript`, replace
`.sparkwing/jobs/check.ts` with this file, and run `sparkwing run check`. The
scaffold's `sparkwing.yaml` entry already names the `Check` entrypoint.
