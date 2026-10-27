# Tagged logs

Hosts upload through an `Uploader` that the controller client binds to one
`blob.LogSubject`. A job attempt and a trigger, plan or eval
unit use separate object prefixes. Eval includes its own lease generation and
the immutable source generation.

`Options.Subject` is required. The sink copies it before opening its spool,
so changes to the caller's value cannot retag the log. Every stored manifest
requires a valid subject. Flat run, node, attempt and generation fields are
removed. `CommittedReader` checks the store-selected subject and object keys
before opening bytes. Physical manifests and object listings grant no read
authority. The object-side reader and abandoned-log sealer are removed.
`Operator.SealAbandonedLogStreams` owns abandoned seals under the maintenance
lease and preserves every committed segment. `AttemptPrefix` is removed. Slot
sizes, upload deadlines and deletion follow [log slot accounting](../../store/STORE-DATA.md#log-slot-accounting).

Dashboard completeness uses the selected attempt's persisted finish time and
`RunSnapshot.StoreNow`. A later job finish or an unrelated attempt cannot extend
that stream's final tail grace. `NodeProgress` and `SealGrace` are removed.
`NodeLogs.NodeLogCompleteness` takes only context, run and job IDs. The shared
grace constant remains `limits.LogSealGrace`.

Each segment and the final manifest is signed separately with its
exact stored size and SHA-256. The final manifest contains the exact committed
segment list and its tagged identity. The sink can mark a lost tail incomplete
without changing the job's result. Failed spool writes or closure mark the final manifest
incomplete, including when a later write succeeds. The store creates each stream
in the transaction that starts its attempt or claims its owner unit, or imports its published
attempt. Hosts open spools without a registration request. Detached uploads commit
only to existing streams. Hosts seal streams with no output as complete and empty.
If retry fences a flush, maintenance seals that attempt
incomplete and run details retain its stream. Failed uploads emit a masked operator
error. Log I/O holds no lock shared across jobs, and native host acceptance and
result reporting never wait for uploads or closure.

`Signer` signs one segment or manifest URL with a fifteen-minute expiry. The
controller supplies the run's persisted retention class after authorizing its
writer. A required callback checks the class lifecycle before any URL is signed.
The signer copies the subject and derives the key from it. `LogPutSigner`
returns URLs and headers for both `blob.Filesystem` and `blob.S3`, bound to
the requested length and digest. The filesystem backend permits only an identical
retry at an existing key. S3 signs a create-only condition and rejects every
overwrite. Index zero selects the final manifest. A 412 alone cannot
establish a matching payload.

The controller must authorize the current writer and resolve the run's persisted
retention class before calling `UploadVerifier.Verify`. The verifier derives one
key, reads bounded bytes and requires successful close, exact size and SHA-256.
It returns no bytes or read URL and creates no reference edge. `RecordSegments`
rechecks the writer before committing that edge. The verifier works with both
object backends; the controller's final log commit uses it for the manifest.
`LogClassReady` is part of the controller's physical log binding. S3 validates
the bucket lifecycle, which must expire the class no sooner than its days.
Filesystem validates the class handled by its expiry pass.
The controller runs filesystem expiry after renewing its maintenance lease. Both
are backstops: Sparkwing deletes log objects itself under [log slot accounting](../../store/STORE-DATA.md#log-slot-accounting).

`CommittedReader` calls `ReadLogSegments` before each segment. It checks the returned
subject, keys and contiguous ranges before reading any object. Each segment must
close successfully and pass digest and raw-size verification before its bytes
reach the response. `ReadResult.Bytes` counts bytes emitted, including partial
writes before an error. The controller must meter that count on success and
failure. It joins the returned ranges to its live ring and polls for follow
requests.
Both filesystem and S3 backends open exact log keys for this reader. S3 response
lengths must fit the shared object bound before the reader consumes their bytes.

Component tests cover lifecycle refusal, expiry, size bounds, replay
verification and committed reads against filesystem and S3 objects. These
proofs do not count as native acceptance receipts.

The append/delete `LogStore` conformance facade and its unused portable suite
are removed. Log tests call the tagged sink, reader and physical expiry APIs.
`TestSegmentReadsApplyFiltersAcrossCuts` checks head, tail, grep and line ranges
across immutable cuts, including combined filters and invalid ranges. Physical
retention has separate expiry tests.

The authenticated signing and commit routes use the configured physical
backend and real tenant transactions.

`internal/blob` owns physical log keys. `LogObjectKey` reserves index zero for
the final manifest and positive indexes for segments. Signing, verification and
reads use its prefix and filename helpers. The former `logseg` key helpers are
removed. The store can use this owner without importing `logseg`.

The dashboard reader uses `Tenant.ReadRunView` to select committed subjects and
passes the request actor to `CommittedReader`. Both physical backends are selected
through `Server.WithDataObjects`. There is no separate S3 log store or run-directory
fallback. Closing a dashboard stream cancels its pending reads.
`TestCommittedDashboardLogsReadRealRecords` drives a real claim, start, signed
filesystem upload, segment commit, exact-offset read and SSE resume.
`TestCommittedDashboardLogsNeverEmitForForeignOrRevokedActors` generates payloads
against the real tenant and verifies that ended authority emits no bytes.
`TestLogSubmissionRoutesCommitOnlyTheirExactAuthorizedSubject` drives an
authenticated claim through signing, filesystem PUTs, segment commits and a
controller GET. It checks exact bytes and generated foreign-subject refusals.

`TestCommittedDashboardLogsRecheckAuthorityBetweenSegments` revokes a real
SQLite capability after the first segment is written. The next segment is
refused before physical I/O. The partial byte count remains exact, and the
reader cannot report completeness.

The backend's separate live-ring interface and the web fallback are removed.
Dashboard streams use the same bound reader for persisted segments and ring
continuation. An unavailable stream has no alternate byte path.
