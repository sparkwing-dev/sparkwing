### Fixed

- **toolchain:** Preserve failures that precede shutdown
  Compiler cancellation suppresses only a termination sent by its process owner.
  External signals and nonzero exits keep their wait status, including through
  the pipeline compiler. A supplied pipe-drain bound does not start a second
  cancellation killer. Grace periods and drain deadlines are unchanged.
