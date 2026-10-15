# Storage rewrite contract

This page describes the target storage contract. The byte backends and
artifact and binary clients implement it. Controller, tenant-store and host integration
are still in progress.

<!-- design:begin -->

Sparkwing's storage contract separates immutable bytes from the records that
authorize a job to read them. The controller signs uploads and downloads.
The object backend is S3 or the filesystem signer.

## Bytes and records

Each object is named by its SHA-256 digest. Uploads verify the expected length
and digest before commit. Downloads verify both at EOF. A missing object or a
checksum mismatch cannot produce a successful output read or artifact stage.
Memo uses a miss and dependency caches start cold when verification fails.

A record names an output, artifact manifest, memo result, dependency cache,
pipeline binary or source bundle. Each reference requires either the exact
uploader's receipt or an authorized read edge. Knowing a digest does not grant
access. Presence queries reveal only objects the caller may read.

Memo and dependency-cache records carry repository and ref scope. Manual
writes cannot overwrite verified branch entries. Memo reads its own scope
before a verified default-branch fallback. Dependency caches try their own
ref, the pull-request base, then the verified default branch.

## Source and package downloads

Runners clone the source repository directly. The controller releases the
run's permitted Git credential. A machine owner can use `--allow-repo` or
`allow_repos` to restrict builds and permit fallback to that machine's Git
credentials. Working-tree snapshots travel as signed source bundles.

Package managers use their upstream registries. To use a separate registry
mirror, set its tool configuration and list the required environment names
in the operator's `run.submit_env_allow`. Network proxy and CA settings remain
available to pipeline processes. Sparkwing provides no Git mirror service
or package proxy.

Local pipeline binaries stay in the local binary cache. Remote binaries use
signed transfers and require authorized build provenance before execution.
The binary client implements that check. Host binding is pending, so remote
hosts build into their machine's binary cache without shared binary transfers.
See [Caching](caching.md) for authoring cache declarations.

## Logs

Logs use immutable segments with their own signed upload path. Log reads pass
through the controller for exact egress accounting. They do not use the blob
download path and remain readable when a team reaches its download cap.

<!-- design:end -->
