# Sparkwing Go SDK

Import `github.com/sparkwing-dev/sparkwing/sparkwing` to declare pipelines.
Go 1.25 or newer is required. The SDK uses the standard library at runtime.
The host owns scheduling, policy, storage, encryption and masking.

SDK versions use `sparkwing/v0.x.y` tags independently of engine releases.
See [the SDK guide](../docs/sdk.md) for the author API and
[the development guide](../DELIVERY.md) for verification and release steps.

Author tests that need the host import
`github.com/sparkwing-dev/sparkwing/sparkwingtest` from the root host module.
That helper has the host's Go minimum and dependency graph.

## Dependencies

- `go.uber.org/goleak` detects goroutines left running by SDK and host tests.
  It is used by test support, and is outside the pipeline runtime import closure.
