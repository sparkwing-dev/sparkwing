### Added

- **pipeline:** `sparkwing pipeline describe --show-withheld` prints withheld planning errors
  A pipeline whose Plan fails, or a TypeScript pipeline that does not load,
  used to report only `details withheld: secrets not yet known`, with no way to
  see the cause. The withheld error and the withheld-output notice now name
  `sparkwing pipeline describe --pipeline <name> --show-withheld`, which prints
  the real error and the SDK's output on your terminal, masked by every secret
  Sparkwing knows. Default output, run logs and the describe cache stay
  withheld, because a failed pipeline's own secret defaults are unknown.
