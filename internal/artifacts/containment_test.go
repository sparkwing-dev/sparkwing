package artifacts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/quick"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
)

func TestStageNeverWritesOutsideWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows symlink privilege is required")
	}
	property := func(seed [16]byte, depth uint8) bool {
		data, store, records := testData()
		workspace, outside := t.TempDir(), t.TempDir()
		payload := append(bytes.Clone(seed[:]), 'x')
		digest := digestOf(payload)
		outsideFile := filepath.Join(outside, "target")
		if err := os.WriteFile(outsideFile, []byte("preserved"), 0o600); err != nil {
			t.Fatal(err)
		}
		parent := "."
		for i := range depth % 6 {
			parent = filepath.Join(parent, fmt.Sprintf("dir-%x-%d", seed[:2], i))
		}
		if err := os.MkdirAll(filepath.Join(workspace, parent), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(outside, filepath.Join(workspace, link)); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"target", filepath.ToSlash(filepath.Join(link, "target")), "../target", outsideFile} {
			body, err := json.Marshal(blob.Manifest{Entries: []blob.ManifestEntry{{Path: path, Digest: digest, Size: int64(len(payload)), Mode: 0o600}}})
			if err != nil {
				t.Fatal(err)
			}
			manifest := digestOf(body)
			store.values[digest], store.values[manifest] = payload, body
			key := blob.RecordKey{Team: "team", Kind: blob.KindManifest, Name: "run"}
			records.result = blob.RecordLookupResult{Found: true, Record: blob.Record{Key: key, Digest: manifest}, Members: []blob.Member{{Team: key.Team, ManifestDigest: manifest, Path: path, Digest: digest, Size: int64(len(payload)), Mode: 0o600}}}
			for _, into := range []string{link, ".", "../" + filepath.Base(outside), outside} {
				n, err := Stage(t.Context(), data, workspace, []Edge{{Record: key, Into: into}})
				if into == "." && path == "target" {
					if err != nil || n != 1 {
						t.Fatalf("contained write: %d, %v", n, err)
					}
				} else if err == nil || n != 0 {
					t.Fatalf("escape %q into %q: %d, %v", path, into, n, err)
				}
				if body, err := os.ReadFile(outsideFile); err != nil || string(body) != "preserved" {
					return false
				}
				if files, err := os.ReadDir(outside); err != nil || len(files) != 1 {
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(66))}); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureNeverPublishesOutsideWorkspace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows symlink privilege is required")
	}
	property := func(seed [32]byte, directory bool) bool {
		data, store, records := testData()
		workspace, outside := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "private"), seed[:], 0o600); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "private")
		if directory {
			target = outside
		}
		if err := os.Symlink(target, filepath.Join(workspace, "link")); err != nil {
			t.Fatal(err)
		}
		warnings := 0
		record, err := Capture(t.Context(), data, workspace, []string{"**"}, func(string) { warnings++ })
		if err != nil || warnings != 1 || len(records.commits) != 1 || len(records.commits[0].Members) != 0 || len(store.puts) != 1 || store.puts[0].Digest != record.Digest {
			return false
		}
		_, published := store.values[digestOf(seed[:])]
		return !published
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 128, Rand: rand.New(rand.NewSource(67))}); err != nil {
		t.Fatal(err)
	}
}
