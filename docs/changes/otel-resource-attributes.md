### Changed

- **controller:** Honor `OTEL_RESOURCE_ATTRIBUTES` on exported telemetry
  The controller now adds the resource attributes named in
  `OTEL_RESOURCE_ATTRIBUTES` to its traces, metrics and logs, so a
  deployment can label them, for example with `k8s.namespace.name` to tell
  environments apart in a shared log store. `OTEL_SERVICE_NAME` still
  sets the service name.
