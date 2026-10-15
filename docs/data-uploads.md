# Data storage

The `swcg` cache-grant protocol and its upload, commit, download and capability
probe routes are removed. Controllers no longer read a `cache-grant-key`
credential or announce `data_download_url` and `direct_data` through services.

The byte-storage components share `blob.Store` for binaries, source bundles,
outputs, artifact manifests, memo and dependency caches. `NewSignedStore` uses
an authenticated `DataSigner` for upload tickets, commits, downloads and presence
queries. `RecordStore` owns logical names and authorized reference edges.
Controller and remote-client adapters for these interfaces remain incomplete.
The removed grant endpoints have no fallback.

The filesystem and S3 backends sign one-object transfers. A PUT binds the expected
size and SHA-256 to a staging object. Commit verifies stored bytes through EOF
and successful close before publishing an immutable `teams/<team>/cas/<sha256>`
object. Retrying an interrupted commit verifies an existing final object too.
Object metadata and a successful physical copy cannot establish a receipt.
The store must recheck live authority and settle the reservation before it
records existence and the exact uploader's receipt.

Downloads verify size and SHA-256 at EOF. Artifact staging fails on missing or
corrupt bytes. Memo becomes a miss and dependency caches run cold. HasMany may
report presence only within the caller's authorized read set.

Local remote-cache reads require explicit profile opt-in. They read authorized
verified-default-branch memo, artifacts and dependency caches, never write shared
records, and never fetch executable pipeline binaries.

Logs have a separate signed segment path and controller-proxied reads. Tagged
subjects distinguish job attempts from trigger, plan and eval units. Controller
batch signing and record integration are still being implemented.

The existing output and job-log handlers retain their own S3 transfer setup.
Those handlers do not restore the removed cache-grant protocol.
