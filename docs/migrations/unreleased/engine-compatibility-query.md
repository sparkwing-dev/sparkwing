## Engine compatibility query

`GET /api/v1/capabilities` returns deployment features and no longer accepts
the `engine_version` query parameter. Remove that parameter from callers.

The OpenAPI `components.schemas.internal.controllerwire.Compatibility` response
type is removed, including
its `api_major`, `engine_release`, `guaranteed_until` and `released_at` fields.
Stop reading engine support windows from this endpoint. Engine selection,
signed version enforcement and runner policy keep their existing behavior.
