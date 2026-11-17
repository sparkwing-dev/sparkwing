### Fixed

- **runner:** Apply completed Cloud awards before another full reconciliation while preserving pending Pod watch work within the bounded claim burst
- **runner:** Preserve capacity container startup timing when node reservation consumes the capacity Pod

### Added

- **runner:** Record Cloud claim receipt, first application, capacity deletion and removal wait timing
