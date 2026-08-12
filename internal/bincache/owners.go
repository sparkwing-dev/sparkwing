package bincache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ownersFile sits beside a cached binary and records which checkouts
// have used it.
//
// A cache key is a content fingerprint, so the entry itself says
// nothing about where it came from. That used to be recoverable by
// reading build paths out of the binary, but builds now pass -trimpath
// precisely so those paths are absent -- which is what lets two
// checkouts share one binary. Recording owners here restores the
// answer to "what is this 90 MB entry for?" that -trimpath removes.
const ownersFile = "owners.json"

// maxOwners bounds the list. Owners accumulate as worktrees come and
// go, and the oldest are the least useful for identifying an entry.
const maxOwners = 10

// Owner is one checkout that has used a cached binary, with how many
// times it has done so. Two owners on one entry is the portable key
// paying off: a worktree reusing the primary checkout's build.
type Owner struct {
	Dir      string    `json:"dir"`
	LastSeen time.Time `json:"last_seen"`
	Uses     int       `json:"uses"`
}

func ownersPath(key string) string {
	entry, err := PipelineEntry(key)
	if err != nil {
		return ""
	}
	return filepath.Join(entry.entryDir(), ownersFile)
}

// Owners returns the checkouts recorded against a cache key, most
// recently seen first. A missing or unreadable file yields nil: this is
// descriptive data, and no caller should fail for lack of it.
func Owners(key string) []Owner {
	return readOwners(ownersPath(key))
}

func readOwners(path string) []Owner {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var owners []Owner
	if err := json.Unmarshal(data, &owners); err != nil {
		return nil
	}
	sort.Slice(owners, func(i, j int) bool {
		return owners[i].LastSeen.After(owners[j].LastSeen)
	})
	return owners
}

// recordOwner notes that sparkwingDir used the leased binary, counting
// the invocation. Called on compiles and on every cache hit, so the
// count answers "how much has this entry actually saved?" -- the
// question that decides whether a 90 MB entry is worth its space.
//
// Errors are swallowed. A read-only filesystem or a lost race costs
// accuracy in `sparkwing cache info` and nothing else.
func recordOwner(path, sparkwingDir string) {
	abs, err := filepath.Abs(sparkwingDir)
	if err != nil {
		return
	}
	if path == "" {
		return
	}
	owners := readOwners(path)

	now := time.Now()
	for i, o := range owners {
		if o.Dir != abs {
			continue
		}
		owners[i].LastSeen = now
		owners[i].Uses = o.Uses + 1
		writeOwners(path, owners)
		return
	}

	owners = append([]Owner{{Dir: abs, LastSeen: now, Uses: 1}}, owners...)
	if len(owners) > maxOwners {
		owners = owners[:maxOwners]
	}
	writeOwners(path, owners)
}

// TotalUses sums the invocations an entry has served across every
// checkout that shares it.
func TotalUses(owners []Owner) int {
	total := 0
	for _, o := range owners {
		total += o.Uses
	}
	return total
}

// writeOwners replaces the file atomically so a concurrent reader never
// sees a half-written list. Concurrent writers are last-write-wins,
// which at worst drops one entry from a display list.
func writeOwners(path string, owners []Owner) {
	data, err := json.Marshal(owners)
	if err != nil {
		return
	}
	writeDescriptiveFile(path, data)
}

// partsFile records the per-input digests that produced a cached
// binary's key, so a later miss can be explained as "this input
// changed" rather than "the hash is different".
const partsFile = "key-parts.json"

func partsPath(key string) string {
	entry, err := PipelineEntry(key)
	if err != nil {
		return ""
	}
	return filepath.Join(entry.entryDir(), partsFile)
}

// recordKeyParts stores the labeled digests behind the leased entry. Best effort:
// without it `cache explain` loses the comparison but nothing else.
func recordKeyParts(path string, parts []KeyPart) {
	if path == "" {
		return
	}
	if _, err := os.Stat(path); err == nil {
		return
	}
	m := make(map[string]string, len(parts))
	for _, p := range parts {
		m[p.Label] = p.Digest
	}
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	writeDescriptiveFile(path, data)
}

func writeDescriptiveFile(path string, data []byte) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".metadata-")
	if err != nil {
		return
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return
	}
	if err := temp.Close(); err != nil {
		return
	}
	_ = replaceCacheMetadata(tempPath, path)
}

// RecordUse updates descriptive metadata while the caller's execution lease
// keeps the entry stable.
func (l *Lease) RecordUse(sparkwingDir string, parts []KeyPart) {
	if l == nil || l.file == nil {
		return
	}
	recordOwner(filepath.Join(l.entry.entryDir(), ownersFile), sparkwingDir)
	recordKeyParts(filepath.Join(l.entry.entryDir(), partsFile), parts)
	now := time.Now()
	_ = os.Chtimes(l.entry.entryDir(), now, now)
}

// StoredKeyParts returns the digests recorded for key, or nil.
func StoredKeyParts(key string) map[string]string {
	path := partsPath(key)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// DiffKeyParts names the inputs that differ between a stored key and
// the current one, which is the direct answer to "why did this
// recompile?". Labels present on one side only are reported as added or
// removed.
func DiffKeyParts(stored map[string]string, current []KeyPart) []string {
	var changed []string
	seen := make(map[string]bool, len(current))
	for _, p := range current {
		seen[p.Label] = true
		was, ok := stored[p.Label]
		switch {
		case !ok:
			changed = append(changed, p.Label+" (new input)")
		case was != p.Digest:
			changed = append(changed, p.Label)
		}
	}
	for label := range stored {
		if !seen[label] {
			changed = append(changed, label+" (no longer an input)")
		}
	}
	sort.Strings(changed)
	return changed
}
