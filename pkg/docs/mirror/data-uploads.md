# Direct data uploads

When the controller has an S3 `--cache-blob-store`, runners can move binary
and artifact bytes between themselves and the bucket. The controller handles
small requests and the storage ledger. `GET /api/v1/services` announces
`direct_data`; a runner with only a cache grant can probe
`GET /api/v1/data/capabilities`. Runners use the cache service on older
controllers. An `fs://` artifact store uses its existing local byte path; it
does not issue S3 or CloudFront URLs. This signing route serves claimed runners.
It has no owner or CLI token path.

The runner must hold a live claim on `run_id` for each request and send a
cache grant minted for that claim. The runner's raw token cannot sign uploads.
A signing grant carries the exact node or trigger claim, including its
generation. Reserve, commit and download check that claim and its token again.
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

The bucket's one-day `pending/` lifecycle removes abandoned bytes. The
controller's hourly storage pass releases expired reservations and removes
their upload rows. Uploads are limited to 500 MiB per object. Unknown-length
artifacts spool to a temporary file before reserve.

For a binary, the final key includes its content hash. `POST /api/v1/data/download`
takes `{kind,key}` and returns `{url,sha256,size,expires}`. A binary read uses
`bin/<input-hash>` as its key; an artifact read names the committed key. The
same route signs legacy cache reads. It applies the team's daily download cap
to both paths. In-cluster callers receive an S3 URL, and callers through the
public ingress receive a CloudFront URL. Cloud runners receive only cloud-built
binaries unless the team owner sets
`trust_local_builds` with `PUT /api/v1/team/build-trust`. The default is
false. `GET /api/v1/team/build-trust` reports the team's choice. Runners
verify the committed digest while reading.
