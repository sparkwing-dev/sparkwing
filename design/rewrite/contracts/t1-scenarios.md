# Acceptance requirements and execution

<!-- design:begin -->

[t1-invariants.md](t1-invariants.md) records product requirements.
[t1-acceptance.md](t1-acceptance.md) defines executable evidence.
[t1-port-inventory.md](t1-port-inventory.md) records historical source-test
migration decisions; its old scenario destinations are historical references.

Concrete tests own their inputs and assertions. There is no scenario catalog,
placement expansion or skip allowance. Selecting a cell executes its driver and
requires a successful result bound to the source revision and runtime platform.
A declared requirement or historical destination supplies no coverage evidence.

<!-- design:end -->
