### Fixed

- **cli + runner:** Name refused submitted-environment variables and explain the repair
  Explicit reserved names in the submit allow-list now produce a CLI warning.
  Controller and runner refusals retain the variable name and usable guidance
  without exposing its value or widening environment permissions.
