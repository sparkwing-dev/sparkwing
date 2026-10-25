package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Manifest names immutable artifact files. The store authorizes its members
// separately; readers require these bytes to describe those same edges.
type Manifest struct {
	Entries []ManifestEntry `json:"entries"`
}

// ManifestEntry carries the file's exact size and mode alongside its digest.
type ManifestEntry struct {
	Path   string `json:"path"`
	Digest Digest `json:"digest"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// decodeManifest verifies the digest and exact agreement with authorized members.
func decodeManifest(body []byte, digest Digest, team string, members []Member) (Manifest, error) {
	sum := sha256.Sum256(body)
	if Digest(hex.EncodeToString(sum[:])) != digest {
		return Manifest{}, ErrIntegrity
	}
	var manifest Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return Manifest{}, ErrIntegrity
	}
	if len(manifest.Entries) != len(members) {
		return Manifest{}, ErrIntegrity
	}
	byPath := make(map[string]Member, len(members))
	for _, member := range members {
		if _, exists := byPath[member.Path]; exists {
			return Manifest{}, ErrIntegrity
		}
		if member.Team != team || member.ManifestDigest != digest || member.Size < 0 || !validDigest(member.Digest) {
			return Manifest{}, ErrIntegrity
		}
		byPath[member.Path] = member
	}
	for _, entry := range manifest.Entries {
		member, ok := byPath[entry.Path]
		if !ok || member.Digest != entry.Digest || member.Size != entry.Size || member.Mode != entry.Mode {
			return Manifest{}, ErrIntegrity
		}
		delete(byPath, entry.Path)
	}
	return manifest, nil
}
