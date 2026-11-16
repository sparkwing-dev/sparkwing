### Fixed

- **cli:** `pipeline new` writes gofmt-clean Go for every shape
  The build-test-deploy, ci-pr-check, release and scheduled-report shapes
  indented their doc-comment diagram with spaces, so `gofmt -l` or golangci-lint
  in a new project failed on the scaffolded file. The Plan comment in every shape
  also named `run.Args`, which `RunContext` does not have, and trigger sources
  that never occur; it now names `run.Git`, `run.Trigger` and the typed inputs
  parameter.
