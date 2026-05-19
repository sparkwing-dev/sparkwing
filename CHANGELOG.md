# Changelog

## Unreleased

### Changed

- `WorkStep.Destructive()` / `.AffectsProduction()` / `.CostsMoney()` replaced
  by `.Risk("destructive")` / `.Risk("prod")` / `.Risk("money")`; labels are
  now author-defined (any kebab-case string works, e.g. `.Risk("rotates-key")`).
  Consumer repos using the old methods must update.
- `--sw-allow-destructive` / `--sw-allow-prod` / `--sw-allow-money` collapsed
  into one `--sw-allow LABEL[,LABEL...]` flag (repeatable; comma-separated
  allowed). Profile `auto_allow` is now a list of labels
  (`auto_allow: [destructive]`) instead of per-marker booleans.

### Removed

- Retired `--sw-retry-of` and `--sw-full`; use `sparkwing runs retry RUN_ID [--failed | --all]`.
