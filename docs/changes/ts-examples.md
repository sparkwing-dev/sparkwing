### Added

- **cli:** `sparkwing examples` lists TypeScript example pipelines beside the Go ones:
  `lint-test-node-ts` installs once and lints and tests in parallel steps.
  `test-matrix-artifacts-deploy-ts` runs a Node version matrix with
  `plan.jobFanOut`, publishes `dist/` and deploys it from `main`.
  `secret-and-cache-ts` tests with a cached npm install and deploys with a
  declared, masked secret, a retry and timeouts. Every listed example now has a
  `language` (`go` or `typescript`) in JSON and in the detail view, and
  `--language typescript` lists only the TypeScript ones. `docs search` finds
  them too, and `info --for-agent` points at `sparkwing examples --language`.
