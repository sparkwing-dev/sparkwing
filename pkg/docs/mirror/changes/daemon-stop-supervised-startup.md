### Fixed

- **cli:** `daemon stop` waits for supervised Linux runners still starting after a controller reopen, and reports incomplete shutdown if their units remain at the stop bound
