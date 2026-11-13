### Added

- **runner:** Trace annotation authorization and controller HTTP phases
  Slow requests also emit host-stamped JSON run-log diagnostics without a
  collector. Human log views hide these records. Existing OTEL settings enable
  bounded-lifetime runner trace export; credentials and payloads are excluded.

### Changed

- **sdk:** Reserve `request_timing` for host diagnostics
  SDK events using this kind are retained as `sdk_request_timing` and stay
  visible in human log output.
