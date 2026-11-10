### Fixed

- **cli:** Report incomplete shutdown when `sparkwing daemon stop` leaves its runner alive
  The error names the runner and explains how to restart the controller, let held work finish and retry the stop.
