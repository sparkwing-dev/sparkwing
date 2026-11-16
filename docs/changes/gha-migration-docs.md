### Docs

- **docs:** Add a GitHub Actions migration guide and a known-limitations page
  `migrate-from-github-actions` maps checkout, toolchains, caches, matrices,
  `needs`, `if`, secrets, artifacts, concurrency, schedules, dispatch inputs,
  deploys on `main`, status checks and notifications to Go and TypeScript
  pipeline code, and marks each one as working today, working differently or
  not yet available. `known-limitations` lists each missing piece with its
  workaround, including shared-runner and secret-handling constraints.
- **docs + examples:** Document run events and add a Slack receiver
  `run-events` covers subscriptions, the event body, delivery and signature
  verification. `examples/slack-run-events` verifies each delivery's Standard
  Webhooks signature and posts it to a Slack incoming webhook.
- **docs:** Label Windows support experimental
  Getting started, the README and the limitations page recommend WSL2 with
  systemd. The native-mode page no longer says Windows fires daemon
  schedules; Windows records schedules and `schedules run` starts one.
