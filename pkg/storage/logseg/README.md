# Tagged logs

`Data` binds one `blob.LogSubject` to `blob.LogTransport`, a signed byte
transport and the store's `RecordSegments` method. `NewSubjectUploader` copies
that identity before requesting URLs. A job attempt and a trigger, plan or eval
unit use separate object prefixes. Eval includes its own lease generation and
the immutable source generation.

`SubjectUploader` verifies compressed bytes, raw size and line coverage before
signing. Batches contain up to `limits.LogUploadBatch` segment slots and one
manifest URL. A batch must preserve the subject, retention class, dense indexes
and expiry. Failed transfers and commits leave the segment retryable. A retry
cannot substitute different bytes under an uncertain immutable write.

Each successful segment commit advances the uncompressed byte offset. The final
manifest contains the exact committed segment list and its tagged identity.
The uploader preserves the first final payload across failures and rejects a
changed final payload. The sink can mark a lost tail incomplete without changing
the job's result.

`CommittedReader` calls `ReadLogSegments` on each read. It checks the returned
subject, keys and contiguous ranges before reading any object. Each segment must
close successfully and pass digest and raw-size verification before its bytes
reach the response. `ReadResult.Bytes` counts bytes emitted, including partial
writes before an error. The controller must meter that count on success and
failure. It joins the returned ranges to its live ring and polls for follow
requests. `Data.Read` is the host-side entry to the controller-proxied stream.

The component tests use fake signing and record interfaces with real compression
and spool files. The store's transactional authorization and log persistence,
controller route and object signing, egress settlement, retention lifecycle
checks and host bindings still need native integration. The existing controller
and host log paths still use the older attempt-only uploader and manifest fields.
Those paths have no adapter into the tagged implementation.
