# Memo materialization

The earlier host-side candidate adapter is removed under decision 28. The
current contract is [C3 memo materialization](c3-protocol-sdk.md#memo-materialization).
Executable-job memo checks run after SkipIf in the shared executor, inside the
job's acquired concurrency slot under its actual execution award. Byte and record
clients provide verified reads and transactional copy authorization. A verified
hit settles the awarded job as memoized without running its body, then releases
its groups. The coordinator acquires no job groups for memo evaluation.
`ReconcileNode` retains retry and coalescing result materialization. The explicit
remote profile remains read-only.

`storage-memo-materialization-failures` covers missing output, refused manifest
copy, transaction rollback and replay through the real store. It requires a
single memoized event on success and no partial result edges after refusal.
