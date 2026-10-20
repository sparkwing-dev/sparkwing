# Memo materialization

The earlier host-side candidate adapter is removed under decision 28. The
current contract is [C3 memo materialization](c3-protocol-sdk.md#memo-materialization).
`ReconcileNode` owns the atomic result copy and lifecycle event. Owner policies
supply a scoped source; byte and record clients provide verified reads and
transactional authorization. The explicit remote profile remains read-only.

`storage-memo-materialization-failures` covers missing output, refused manifest
copy, transaction rollback and replay through the real store. It requires a
single memoized event on success and no partial result edges after refusal.
