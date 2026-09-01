package bincache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const ownersFile = "owners.json"

const maxOwners = 10

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

func TotalUses(owners []Owner) int {
	total := 0
	for _, o := range owners {
		total += o.Uses
	}
	return total
}

func writeOwners(path string, owners []Owner) {
	data, err := json.Marshal(owners)
	if err != nil {
		return
	}
	writeDescriptiveFile(path, data)
}

const partsFile = "key-parts.json"

func partsPath(key string) string {
	entry, err := PipelineEntry(key)
	if err != nil {
		return ""
	}
	return filepath.Join(entry.entryDir(), partsFile)
}

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

func (l *Lease) RecordUse(sparkwingDir string, parts []KeyPart) {
	if l == nil || l.file == nil {
		return
	}
	recordOwner(filepath.Join(l.entry.entryDir(), ownersFile), sparkwingDir)
	recordKeyParts(filepath.Join(l.entry.entryDir(), partsFile), parts)
	now := time.Now()
	_ = os.Chtimes(l.entry.entryDir(), now, now)
}

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
