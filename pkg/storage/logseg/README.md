# Tagged logs

`Data` binds one `blob.LogSubject` to `blob.LogTransport`, a signed byte
transport and the store's `RecordSegments` method. `NewSubjectUploader` copies
that identity before requesting URLs. A job attempt and a trigger, plan or eval
unit use separate object prefixes. Eval includes its own lease generation and
the immutable source generation.

`SubjectUploader` verifies compressed bytes, raw size and line coverage before
signing. Batches contain up to `limits.LogUploadBatch` segment slots and one
manifest URL. Every URL carries its required signed headers. A batch must
preserve the subject, retention class, dense indexes and expiry. Failed transfers
and commits retain the pending payload. A retry cannot substitute different
bytes under an uncertain immutable write.

Each successful segment commit advances the uncompressed byte offset. The final
manifest contains the exact committed segment list and its tagged identity.
The uploader preserves the first final payload across failures and rejects a
changed final payload. The sink can mark a lost tail incomplete without changing
the job's result.

`BatchSigner` assembles up to sixteen segment URLs and one manifest URL, all
with the same fifteen-minute expiry. The controller supplies the run's persisted
retention class after authorizing its writer. A required callback checks the
class lifecycle before any URL is signed. The signer copies the subject and
derives every key from it. `LogPutSigner` returns URLs and headers for both
`blob.Filesystem` and `blob.S3`. The filesystem backend permits only an identical
retry at an existing key. S3 signs a create-only condition and rejects every
overwrite. A 412 leaves the segment or manifest uncommitted. Recovery after an
uncertain S3 PUT requires controller verification of the existing bytes.
`blob.S3.LogClassReady` supplies the lifecycle callback for the S3 bucket.
The filesystem backend exposes an expiry pass for the controller's maintenance
lease. Its scheduling must be connected before enabling filesystem log signing.

`CommittedReader` calls `ReadLogSegments` on each read. It checks the returned
subject, keys and contiguous ranges before reading any object. Each segment must
close successfully and pass digest and raw-size verification before its bytes
reach the response. `ReadResult.Bytes` counts bytes emitted, including partial
writes before an error. The controller must meter that count on success and
failure. It joins the returned ranges to its live ring and polls for follow
requests. `Data.Read` is the host-side entry to the controller-proxied stream.
Both filesystem and S3 backends open exact log keys for this reader. S3 response
lengths must fit the shared object bound before the reader consumes their bytes.

The component tests include a filesystem signer and HTTP handler round trip
through the real uploader, sink, compressed files and committed reader. Only
record persistence, authorization and controller transport are fakes. Separate
tests exercise lifecycle refusal, expiry, size bounds, concurrent immutable
uploads and uncertain commit retries. S3 tests run the real presigner, transfer
client, uploader and committed reader with fake object responses and records.
They cover segment and manifest conflicts, lost PUT responses and failed record
commits. These proofs do not count as native acceptance receipts.

The store's transactional authorization and log persistence, controller routes,
egress settlement, retention scheduling and host bindings still need integration.
S3 log signing still needs verified replay after an uncertain upload and admission
for future slots whose payload size and digest are unknown. The existing controller and host paths still
use the older attempt-only uploader and manifest fields. Those paths have no
adapter into the tagged implementation.
