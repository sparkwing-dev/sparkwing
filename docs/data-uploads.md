# Direct data uploads

When the controller has an S3 `--cache-blob-store`, runners can move binary
and artifact bytes between themselves and the bucket. The controller handles
small requests and the storage ledger. `GET /api/v1/services` announces
`direct_data`. Authenticated callers can probe
`GET /api/v1/data/capabilities`. An `fs://` artifact store uses its existing
local byte path; it does not issue S3 or CloudFront URLs. This signing route
serves claimed runners.
Source bundles have a separate pretrigger user-token path described below.

The runner must hold a live claim on `run_id` for each request and send a
cache grant minted for that claim. The runner's raw token cannot sign uploads.
A signing grant carries the exact node or trigger claim, including its
generation. Reserve and commit check that claim and its token again.
Revoking the token stops new signed URLs, even while the claim remains live.
Signing requests share the team's controller request budget across grants and
answer `429` with `Retry-After` when it is spent.
The pending row fixes the run ID and claimant. The
operator's metered marker classifies a token as cloud for build trust. Keep
cloud tokens on trusted machines because that marker controls provenance.

1. `POST /api/v1/data/upload` takes `{kind,key,size,sha256,run_id}`. `kind` is
   `binary` or `artifact`. Binary keys are `bin/<input-hash>/<sha256>`;
   artifact keys are `artifacts/blobs/<sha256>` or
   `artifacts/manifests/<sha256>`. The controller reserves the declared bytes
   in the team's cache share and returns `{upload_id,url,headers,expires_at}`.
   The URL addresses exactly `pending/<upload_id>`. Its SigV4 signature covers
   `Content-Length` and `x-amz-checksum-sha256`.
2. The runner sends a PUT to that URL with the returned headers and exactly
   the declared bytes. It sends no controller bearer to S3.
3. `POST /api/v1/data/commit` takes `{upload_id,run_id}`. The controller HEADs
   the pending object with checksum mode enabled. Size and SHA-256 must match
   the declaration. It copies to the team's immutable key with
   `If-None-Match: *`, storing uploader, provenance, digest and upload ID as
   object metadata. The final key includes `cloud/` or `local/` after the team
   prefix. The controller then publishes a database object row and moves the
   reservation into used bytes. Direct readers cannot see the key until that
   row commits. If a copy completed before the database commit, a new
   reservation can adopt it only when S3 reports the same size, checksum and
   provenance. The committed row keeps the original uploader from S3 metadata;
   the new reservation records who recovered it.

For `kind: source`, a user token with `runs.write` may reserve and commit
`sources/<bundle-sha256>/<random-submission-id>` before a run exists, with
`run_id: ""`. The source takes a free-team slot without incrementing the run
count; the later trigger uses that same slot. The controller atomically binds
the committed upload row to exactly one run from the same team, uploader and
token. Source reads require the run's live capability and a permitted read edge.
A retry must upload a new bundle. `runs retry` and Cloud
`RunAndAwait` children cannot inherit an older run's one-run bundle; both
are refused before admission. Submit a new Cloud run from the checkout
(`sparkwing pipeline trigger <pipeline> --profile <cloud-profile> --working-tree`) to upload fresh bytes. A source consumes the cache share of the
team's 1 GiB free allowance and is limited to 500 MiB. The hourly storage
pass removes an unused source 24 hours after commit, or a bound source 24
hours after the run finishes. Pending and running runs retain their sources until the bucket's absolute
90-day lifecycle backstop, including unusually long active runs. It deletes S3 first, then removes the upload and object rows and reconciles
the byte count.

The bucket's one-day `pending/` lifecycle removes abandoned bytes. The
controller's hourly storage pass releases expired reservations and removes
their upload rows. Uploads are limited to 500 MiB per object. Unknown-length
artifacts spool to a temporary file before reserve.

Downloads use the digest-based capability route described in
[Data downloads](api.md#data-downloads). The upload cache grant cannot sign a
download. Its old `{kind,key}` download request is removed. Storage reads
require the caller's current read set. Artifact reads follow that authority.
Cloud binary execution also requires trusted provenance unless the team
owner enables `trust_local_builds`.

A controller-dispatched node's claim token writes cache under its run's
GitHub repository ID and git ref, which the controller reads from the run and
never from the request, as GitHub Actions scopes caches: `GITHUB_REF` for a
GitHub App trigger, otherwise the run's branch. A repository's ref holds
one binary per input hash: the first committed wins, and a second is refused
`409`. A claim whose run names no repository or ref writes no binary, and
still writes artifacts. Other upload callers write only binaries no claim wrote. Artifact upload keys name their content hash. Data read authority remains separate.
