package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type artifactTestReader struct {
	read     func(string, string) ([]blob.ManifestEntry, error)
	download func(io.Writer) (blob.ManifestEntry, error)
}

func (r artifactTestReader) ReadRun(context.Context, string) (controllerwire.RunDetail, error) {
	return controllerwire.RunDetail{ID: "canonical", Jobs: []controllerwire.JobDetail{{ID: "build"}, {ID: "test"}}}, nil
}

func (r artifactTestReader) ReadArtifacts(_ context.Context, run, job string) ([]blob.ManifestEntry, error) {
	return r.read(run, job)
}

func (r artifactTestReader) DownloadArtifact(_ context.Context, _, _, _ string, out io.Writer) (blob.ManifestEntry, error) {
	return r.download(out)
}

func TestRunArtifactsListsNDJSONAndPlainPaths(t *testing.T) {
	reader := artifactTestReader{read: func(run, job string) ([]blob.ManifestEntry, error) {
		if run != "canonical" {
			t.Fatalf("run = %q", run)
		}
		if job == "test" {
			return nil, nil
		}
		return []blob.ManifestEntry{{Path: "dist/a", Size: 3}, {Path: "dist/b", Size: 4}}, nil
	}}
	for _, mode := range []string{"json", "plain", "pretty"} {
		var out bytes.Buffer
		if err := observeRunArtifacts(t.Context(), logInvocation(t, "artifacts", "--run", "1", "-o", mode), reader, &out); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 2 {
			t.Fatalf("records = %q", out.String())
		}
		if mode == "json" {
			for _, line := range lines {
				var row artifactRecord
				if err := json.Unmarshal([]byte(line), &row); err != nil || row.RunID != "canonical" || row.JobID != "build" {
					t.Fatalf("record = %+v, %v", row, err)
				}
			}
		} else if mode == "plain" && out.String() != "dist/a\ndist/b\n" {
			t.Fatalf("plain paths = %q", out.String())
		}
	}
}

func TestRunArtifactsDownloadPreservesExistingFilesAndRemovesFailedBytes(t *testing.T) {
	for _, fail := range []bool{false, true} {
		dest := filepath.Join(t.TempDir(), "artifact")
		reader := artifactTestReader{download: func(out io.Writer) (blob.ManifestEntry, error) {
			if _, err := io.WriteString(out, "bytes"); err != nil {
				return blob.ManifestEntry{}, err
			}
			if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unverified download appeared at destination: %v", err)
			}
			if fail {
				return blob.ManifestEntry{}, blob.ErrIntegrity
			}
			return blob.ManifestEntry{Path: "dist/file", Size: 5}, nil
		}}
		inv := logInvocation(t, "artifacts", "--run", "run", "--job", "build", "--path", "dist/file", "--dest", dest, "-o", "json")
		var out bytes.Buffer
		err := observeRunArtifacts(t.Context(), inv, reader, &out)
		entries, listErr := os.ReadDir(filepath.Dir(dest))
		wantEntries := 1
		if fail {
			wantEntries = 0
		}
		if listErr != nil || len(entries) != wantEntries {
			t.Fatalf("download left temporary files: %v, %v", entries, listErr)
		}
		if fail {
			if err == nil || out.Len() != 0 {
				t.Fatalf("failure = %v, %s", err, &out)
			}
			if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed download remains: %v", err)
			}
			continue
		}
		if err != nil || !strings.Contains(out.String(), `"saved_to"`) {
			t.Fatalf("download = %v, %s", err, &out)
		}
		if err := observeRunArtifacts(t.Context(), inv, reader, io.Discard); err == nil {
			t.Fatal("overwrote existing destination")
		}
		body, err := os.ReadFile(dest)
		if err != nil || string(body) != "bytes" {
			t.Fatalf("saved bytes = %q, %v", body, err)
		}
	}
}

func TestRunArtifactsQuotesControlCharactersInTextListings(t *testing.T) {
	path := "dist/\x1b[31mfile\a\nnext\tline"
	record := artifactRecord{ManifestEntry: blob.ManifestEntry{Path: path}, JobID: "build"}
	for _, mode := range []string{"pretty", "plain", "json"} {
		var out bytes.Buffer
		if err := writeArtifact(&out, record, mode); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), "\n") != 1 || strings.ContainsAny(out.String(), "\x1b\a") {
			t.Fatalf("unsafe %s listing: %q", mode, out.String())
		}
		if mode == "json" {
			var decoded artifactRecord
			if err := json.Unmarshal(out.Bytes(), &decoded); err != nil || decoded.Path != path {
				t.Fatalf("JSON changed the path: %+v, %v", decoded, err)
			}
		} else if !strings.Contains(out.String(), strconv.Quote(path)) {
			t.Fatalf("unquoted %s path: %q", mode, out.String())
		}
	}
}

func TestRunArtifactsDoesNotReplaceADestinationCreatedDuringDownload(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "artifact")
	reader := artifactTestReader{download: func(out io.Writer) (blob.ManifestEntry, error) {
		if _, err := io.WriteString(out, "download"); err != nil {
			return blob.ManifestEntry{}, err
		}
		return blob.ManifestEntry{Path: "file"}, os.WriteFile(dest, []byte("existing"), 0o600)
	}}
	inv := logInvocation(t, "artifacts", "--run", "run", "--job", "build", "--path", "file", "--dest", dest)
	if err := observeRunArtifacts(t.Context(), inv, reader, io.Discard); !errors.Is(err, os.ErrExist) {
		t.Fatalf("destination was not refused: %v", err)
	}
	body, err := os.ReadFile(dest)
	if err != nil || string(body) != "existing" {
		t.Fatalf("destination was replaced: %q, %v", body, err)
	}
}

func TestRunArtifactsDoesNotPublishACancelledDownload(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dest := filepath.Join(t.TempDir(), "artifact")
	reader := artifactTestReader{download: func(out io.Writer) (blob.ManifestEntry, error) {
		if _, err := io.WriteString(out, "download"); err != nil {
			return blob.ManifestEntry{}, err
		}
		cancel()
		return blob.ManifestEntry{Path: "file"}, nil
	}}
	inv := logInvocation(t, "artifacts", "--run", "run", "--job", "build", "--path", "file", "--dest", dest)
	if err := observeRunArtifacts(ctx, inv, reader, io.Discard); err == nil {
		t.Fatal("cancelled download succeeded")
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancelled download left files: %v, %v", entries, err)
	}
}

func TestRunArtifactsRejectsIncompleteDownloadFlags(t *testing.T) {
	for _, args := range [][]string{{"--path", "file"}, {"--dest", "file"}, {"--path", "file", "--dest", "saved"}} {
		inv := logInvocation(t, append([]string{"artifacts", "--run", "run"}, args...)...)
		if err := validateRunArtifacts(inv); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
