### Fixed

- **orchestrator:** Keep cancelled jobs in their concurrency slots while cleanup drains
  A following deploy waits for rollback to finish instead of racing its GitOps pins.
