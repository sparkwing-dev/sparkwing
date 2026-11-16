# test-matrix-artifacts-deploy-ts

The usual GitHub Actions workflow in one TypeScript pipeline: a test matrix over
Node versions, a build that publishes `dist/`, and a deploy that receives
`dist/` and runs only from `main`.

- `plan.jobFanOut` declares one test job per Node version. Each runs its suite
  through `npx node@<version>`, because jobs use the host's toolchain.
- `.artifacts("dist/**")` publishes the build output and `.consumes(build)`
  stages it into the deploy job's workspace.
- The deploy job's `skipIf` reads the run's branch, which a local run takes
  from the checked-out branch, so the deploy is skipped everywhere but `main`.
- A box-scoped concurrency group makes these jobs take turns on one machine,
  because a local run's jobs share your working tree.

To use it, scaffold with `sparkwing pipeline new --pipeline ci --template typescript`,
replace `.sparkwing/jobs/ci.ts` with this file, and add `scripts/deploy.sh`.
Run it with `sparkwing run ci`, then download the build with
`sparkwing runs artifacts --run <id> --job build`.
