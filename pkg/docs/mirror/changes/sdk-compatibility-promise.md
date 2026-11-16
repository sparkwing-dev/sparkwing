### Docs

- **sdk:** State the SDK compatibility promise and the release cadence
  The Go author SDK packages and `@sparkwing/sdk` take additive changes only;
  an unavoidable break ships with a migration guide and a scripted
  `sparkwing pipeline upgrade` path. `sparkwing/runtime`, `sparkwing/protocol`
  and `sparkwing/testsupport` are not covered. Releases ship on a weekly train,
  breaking changes ship only in its Monday minor release and never in a patch,
  and an SDK pin keeps its engine for that engine's twelve-month support
  window. See `VERSIONING.md` and `sparkwing docs read --topic versioning`.
