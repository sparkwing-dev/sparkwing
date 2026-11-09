### Fixed

- **release:** Prepare dashboard and SDK dependencies before local release tests
  Detached release checkouts now install the frozen web and TypeScript SDK
  lockfiles and run the SDK suite before the Go acceptance tests.
  Serial verification can run for one hour while retaining its 40-minute budget
  warning and lint deadline.
