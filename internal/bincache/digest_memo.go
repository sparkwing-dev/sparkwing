package bincache

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// perf: a replace target's key re-read every file (37 MB in the sparkwing repo, 300-700 ms under load) on each
// SDK install, twice per local run. Digests are kept per directory under each file's exact identity
// (device, inode, size, modification and change time), so an unchanged file is not read again.
// hack: go test binaries hash fixture trees in a real home; they keep no records.
var digestMemoEnabled = !strings.HasSuffix(filepath.Base(os.Args[0]), ".test")

type digestMemo struct {
	path    string
	mu      sync.Mutex
	known   map[string]string
	current map[string]string
}

func openDigestMemo(dir string) *digestMemo {
	if !digestMemoEnabled {
		return nil
	}
	sum := sha256.Sum256([]byte(dir))
	memo := &digestMemo{
		path:    filepath.Join(SparkwingHome(), "cache", "file-digests", hex.EncodeToString(sum[:12])),
		known:   map[string]string{},
		current: map[string]string{},
	}
	file, err := os.Open(memo.path)
	if err != nil {
		return memo
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		if rel, entry, ok := strings.Cut(scanner.Text(), "\x00"); ok {
			memo.known[rel] = entry
		}
	}
	return memo
}

func (m *digestMemo) lookup(rel string, info os.FileInfo) ([]byte, string, bool) {
	if m == nil {
		return nil, "", false
	}
	identity, stable := fileIdentity(info, time.Now())
	if !stable {
		return nil, "", false
	}
	m.mu.Lock()
	entry := m.known[rel]
	m.mu.Unlock()
	if recorded, digest, ok := strings.Cut(entry, " "); ok && recorded == identity {
		if sum, err := hex.DecodeString(digest); err == nil && len(sum) == sha256.Size {
			m.remember(rel, entry)
			return sum, identity, true
		}
	}
	return nil, identity, false
}

func (m *digestMemo) remember(rel, entry string) {
	m.mu.Lock()
	m.current[rel] = entry
	m.mu.Unlock()
}

func (m *digestMemo) record(rel, identity string, sum []byte) {
	if m != nil && identity != "" {
		m.remember(rel, identity+" "+hex.EncodeToString(sum))
	}
}

// safety: a lost or torn record only costs rehashing; a reader requires an exact identity and a full digest.
func (m *digestMemo) save() {
	if m == nil || len(m.current) == 0 || sameEntries(m.known, m.current) {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return
	}
	stage, err := os.CreateTemp(filepath.Dir(m.path), ".digests-")
	if err != nil {
		return
	}
	writer := bufio.NewWriter(stage)
	for rel, entry := range m.current {
		fmt.Fprintf(writer, "%s\x00%s\n", rel, entry)
	}
	if writer.Flush() != nil || stage.Close() != nil || os.Rename(stage.Name(), m.path) != nil {
		discardStage(stage.Name())
	}
}

func sameEntries(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// safety: a stage left behind only costs disk next to a memo that was never required, so it is logged, not returned.
func discardStage(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Default().Warn("bincache: digest memo stage outlived its write", "path", path, "error", err)
	}
}
