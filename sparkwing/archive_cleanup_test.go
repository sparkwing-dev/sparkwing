package sparkwing

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStagedExtractionPreservesCleanupFailure(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	rejected := errors.New("archive rejected")
	err := extractIntoDirStaged(filepath.Join(parent, "cache"), "stage-*", func(string) error {
		if err := os.RemoveAll(parent); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parent, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return rejected
	})
	if !errors.Is(err, rejected) || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("extract error = %v, want rejection and cleanup ENOTDIR", err)
	}
}

func TestExtractionPreservesRequiredMutationFailure(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		headers []*tar.Header
		cause   error
	}{
		{"remove nonempty directory", []*tar.Header{
			{Name: "folder", Typeflag: tar.TypeDir, Mode: 0o700},
			{Name: "folder/child", Typeflag: tar.TypeReg, Mode: 0o600},
			{Name: "folder", Typeflag: tar.TypeReg, Mode: 0o600},
		}, syscall.ENOTEMPTY},
		{"restore mode on missing target", []*tar.Header{
			{Name: "folder", Typeflag: tar.TypeDir, Mode: 0o700},
			{Name: "folder", Typeflag: tar.TypeSymlink, Linkname: "missing"},
		}, os.ErrNotExist},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			for _, header := range testCase.headers {
				if err := writer.WriteHeader(header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			err := extractTarInRoot(tar.NewReader(&archive), t.TempDir(), tarExtractPolicy{allowSymlinks: true})
			if !errors.Is(err, testCase.cause) {
				t.Fatalf("extraction error = %v, want %v", err, testCase.cause)
			}
		})
	}
}

func TestExtractionRestoresChildModesBeforeRestrictiveParent(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory search permissions")
	}
	destination := t.TempDir()
	parent := filepath.Join(destination, "parent")
	t.Cleanup(func() {
		if err := os.Chmod(parent, 0o700); err != nil {
			t.Error(err)
		}
	})
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, header := range []*tar.Header{
		{Name: "parent/child", Typeflag: tar.TypeDir, Mode: 0o500},
		{Name: "parent", Typeflag: tar.TypeDir, Mode: 0},
	} {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractTarInRoot(tar.NewReader(&archive), destination, tarExtractPolicy{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(parent, "child"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("child mode = %o, want 500", info.Mode().Perm())
	}
}
