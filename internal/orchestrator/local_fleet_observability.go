package orchestrator

import "encoding/json"

func fleetSourceSnapshotPayload(opts Options) []byte {
	payload, _ := json.Marshal(struct {
		Commit         string `json:"commit"`
		ManifestDigest string `json:"manifest_digest"`
		Files          int    `json:"files"`
		SourceBytes    int64  `json:"source_bytes"`
		BundleBytes    int64  `json:"bundle_bytes"`
	}{
		Commit: opts.FleetSourceSHA, ManifestDigest: opts.FleetSourceManifestDigest, Files: opts.FleetSourceFiles,
		SourceBytes: opts.FleetSourceBytes, BundleBytes: opts.FleetBundleBytes,
	})
	return payload
}
