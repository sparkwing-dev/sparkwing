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

The removed `github.com/sparkwing-dev/sparkwing/pkg/match.ToolPrefix` constant
is now `github.com/sparkwing-dev/sparkwing/sparkwing/protocol/labels.ToolPrefix`.
Update the import to the SDK-owned labels package.

Go SDK tags use `sparkwing/v0.x.y`. Engine tags remain `v0.x.y`.

### OpenAPI schema names

Generated OpenAPI component identifiers follow the moved Go types. JSON fields
and protocol messages keep their shapes. Regenerate clients that use these
component identifiers after upgrading the engine. The removed identifiers are:

- `components.schemas.internal.apierr.Error`
- `components.schemas.internal.nodeproto.Annotation`
- `components.schemas.internal.nodeproto.AnnotationRequest`
- `components.schemas.internal.nodeproto.ApprovalSpec`
- `components.schemas.internal.nodeproto.ArgumentDescription`
- `components.schemas.internal.nodeproto.ArtifactInput`
- `components.schemas.internal.nodeproto.CacheSpec`
- `components.schemas.internal.nodeproto.ChildPipeline`
- `components.schemas.internal.nodeproto.ClosureSpec`
- `components.schemas.internal.nodeproto.ConcurrencySpec`
- `components.schemas.internal.nodeproto.EnvValue`
- `components.schemas.internal.nodeproto.JobFailure`
- `components.schemas.internal.nodeproto.JobSpec`
- `components.schemas.internal.nodeproto.MaskRequest`
- `components.schemas.internal.nodeproto.MemoInput`
- `components.schemas.internal.nodeproto.MemoSpec`
- `components.schemas.internal.nodeproto.PipelineReference`
- `components.schemas.internal.nodeproto.RetrySpec`
- `components.schemas.internal.nodeproto.RunnerIdentity`
- `components.schemas.internal.nodeproto.ServiceAcknowledged`
- `components.schemas.internal.nodeproto.SourceSpec`
- `components.schemas.internal.nodeproto.StepSpec`
- `components.schemas.internal.nodeproto.SummaryRequest`
- `components.schemas.internal.runspec.Setting...github.com.sparkwing-dev.sparkwing.internal.nodeproto.SourceSpec.`
