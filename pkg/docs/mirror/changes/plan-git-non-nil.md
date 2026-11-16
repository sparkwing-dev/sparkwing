### Fixed

- **sdk:** `RunContext.Git` is never nil in a Go pipeline's Plan
  Discovery, `pipeline describe` and planning calls without git facts passed a
  nil `Git`, so reading `run.Git.Branch` in Plan panicked and every surface
  printed only "details withheld". Plan now receives a `Git` rooted at the work
  tree whose data fields are empty until a run supplies them; its live methods
  such as `IsDirty` still work.
