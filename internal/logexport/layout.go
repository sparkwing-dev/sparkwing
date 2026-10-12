package logexport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
)

// safety: these name the layout sparkwing-logs v0.66 and earlier wrote and no code writes any more: runs/<run>/
// on its volume with a metadata file and .seals/ records, and index/runs/<run>.json in its archive.
const (
	runMetaFile  = ".sparkwing-run"
	sealsDir     = ".seals"
	indexRunsRel = "index/runs/"
	recordSeal   = "seal"
)

type archivedFile struct {
	Rel string `json:"rel"`
}

type runMeta struct {
	Team string `json:"team,omitempty"`
	Free bool   `json:"free,omitempty"`
}

type runIndex struct {
	Team  string         `json:"team,omitempty"`
	Free  bool           `json:"free,omitempty"`
	Files []archivedFile `json:"files"`
}

type sealRecord struct {
	Kind    string    `json:"kind"`
	File    string    `json:"file"`
	Stream  string    `json:"stream"`
	At      time.Time `json:"at"`
	Seal    *seal     `json:"seal,omitempty"`
	Missing int64     `json:"missing,omitempty"`
}

type seal struct {
	Dropped int64 `json:"dropped"`
}

func indexRunRel(runID string) string { return indexRunsRel + runID + ".json" }

func runObjectRel(runID, rel string) string { return "runs/" + runID + "/" + rel }

func readLogFile(root *os.Root, path string) ([]byte, error) {
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(f)
	return data, errors.Join(readErr, f.Close())
}

func readDirAt(root *os.Root, path string) ([]fs.DirEntry, error) {
	d, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	entries, err := d.ReadDir(-1)
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, nil
}

// safety: a torn metadata file records no team, which the export treats as the default team's run; it never
// names another team.
func readRunMeta(root *os.Root, runID string) runMeta {
	var m runMeta
	data, err := readLogFile(root, filepath.Join(runID, runMetaFile))
	if err != nil || json.Unmarshal(data, &m) != nil || (m.Team != "" && !teamblob.ValidTeam(m.Team)) {
		return runMeta{}
	}
	return m
}

func parseRunIndex(runID string, data []byte) (runIndex, error) {
	var idx runIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return idx, fmt.Errorf("index for run %s: %w", runID, err)
	}
	if idx.Team != "" && !teamblob.ValidTeam(idx.Team) {
		return idx, fmt.Errorf("index for run %s names %q, which is not a team", runID, idx.Team)
	}
	return idx, nil
}

// safety: an index names files by path, so one that climbs out of the run or names the metadata file is
// refused rather than read.
func safeArchivedRel(rel string) bool {
	if rel == "" || rel == runMetaFile || strings.HasPrefix(rel, "/") || strings.Contains(rel, "\\") {
		return false
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return strings.HasSuffix(rel, ".log")
}

type localFile struct {
	rel string
}

func runFiles(root *os.Root, runID string) ([]localFile, error) {
	var out []localFile
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		entries, err := readDirAt(root, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			if e.IsDir() {
				if err := walk(filepath.Join(dir, e.Name()), childRel); err != nil {
					return err
				}
				continue
			}
			if safeArchivedRel(childRel) {
				out = append(out, localFile{rel: childRel})
			}
		}
		return nil
	}
	return out, walk(runID, "")
}

// safety: a torn last line from a crash mid-write is skipped rather than hiding every record before it.
func decodeSealRecords(data []byte) []sealRecord {
	var out []sealRecord
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var rec sealRecord
		if json.Unmarshal(sc.Bytes(), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out
}
