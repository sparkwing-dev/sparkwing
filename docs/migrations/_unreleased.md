# Migrating to the next release

## Pipelines compile without cgo

Sparkwing compiles a Go pipeline with `CGO_ENABLED=0` when the environment does
not set `CGO_ENABLED`. Earlier releases left the choice to Go, which enables cgo
whenever a C compiler is on `PATH`. Without cgo, `net` uses Go's own DNS
resolver and `os/user` reads `/etc/passwd` and `/etc/group` directly.

A pipeline whose `.sparkwing/` module imports a package written with cgo (a SQLite
driver built on the C library, for example) now fails to compile with
`build constraints exclude all Go files` or undefined C symbols. Set
`CGO_ENABLED=1` where the pipeline runs:

```bash
CGO_ENABLED=1 sparkwing run build
```

In GitHub Actions, set it on the step or job (`env: CGO_ENABLED: "1"`). A
run cannot submit `CGO_*` names to Cloud or shared runners, so there the
setting comes from the runner's own environment; a self-hosted runner sets
it in the service environment it starts with.
`go env -w CGO_ENABLED=1` does not count: Sparkwing reads the process
environment, and its `CGO_ENABLED=0` overrides Go's configuration file.

Commands your pipeline runs, such as `go build` through `sparkwing.Exec`, are
unaffected; they inherit the job's environment, not the pipeline compile's.
