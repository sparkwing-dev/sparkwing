### Fixed

- **cli:** Update also replaces running on-demand controllers
  `sparkwing update` now drains and replaces a running controller even when no
  login service is installed. Held local jobs keep running under the old runner,
  which stops taking new claims and exits after acknowledging its results.
  A stopped controller stays stopped.
