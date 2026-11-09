## Lean Go SDK module

SDK-only pipeline modules now require `github.com/sparkwing-dev/sparkwing/sparkwing`
instead of the root `github.com/sparkwing-dev/sparkwing` module. Existing SDK
source imports stay unchanged. Pin a published SDK version, then run `go mod tidy`
to regenerate sums. Remove the old root requirement when the pipeline only uses
SDK packages, because old root versions still contain the same import path.

Author tests change their import from
`github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest` to
`github.com/sparkwing-dev/sparkwing/sparkwingtest`. These tests still require the
root host module and its Go minimum. Upgrade that host requirement to a version
containing the split before adding the nested SDK requirement.

Source development uses the committed `go.work` file. Root-only `go test ./...`
no longer runs SDK tests. Run `GOWORK=off go -C sparkwing test ./...` as well.
The root manifest has no replacements. A workspace-off build uses the published
SDK pin. Before the first SDK release, checkout verification uses a temporary
modfile replacement rather than changing the publishable root manifest.

Go SDK tags use `sparkwing/v0.x.y`. Engine tags remain `v0.x.y`.
