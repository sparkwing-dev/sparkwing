### Fixed

- **cli:** `pipeline new --template typescript` writes `.sparkwing/tsconfig.json`
  It also adds `typescript` as a locked development dependency, so
  `sparkwing pipeline upgrade` type-checks a new TypeScript pipeline with
  `tsc --noEmit` instead of skipping that check. An existing `tsconfig.json` is
  kept, and a project without one still upgrades with describe and plan as its check.
