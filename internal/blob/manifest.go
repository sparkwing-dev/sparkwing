package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"path"
	"path/filepath"
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
	if err := CheckManifestMembers(team, digest, members); err != nil {
		return Manifest{}, err
	}
	byPath := make(map[string]Member, len(members))
	for _, member := range members {
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

// CheckManifestMembers validates manifest identity, file metadata and stageable paths.
func CheckManifestMembers(team string, digest Digest, members []Member) error {
	paths := make(map[string]bool, len(members))
	for _, member := range members {
		if member.Team != team || member.ManifestDigest != digest || !validDigest(member.Digest) || member.Size < 0 {
			return ErrIntegrity
		}
		if member.Path == "." || !fs.ValidPath(member.Path) || !filepath.IsLocal(filepath.FromSlash(member.Path)) || paths[member.Path] {
			return ErrIntegrity
		}
		paths[member.Path] = true
	}
	for name := range paths {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if paths[parent] {
				return ErrIntegrity
			}
		}
	}
	return nil
}
