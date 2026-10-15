package depcache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// WriteArchive streams a dependency directory as a gzip-compressed tar archive.
func WriteArchive(w io.Writer, dir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}

		var link string
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		case !info.Mode().IsRegular() && !info.IsDir():
			return nil
		}

		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// #nosec G122 -- a TOCTOU swap here needs write access to this user's own dependency cache, so it is accepted
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

func extractArchive(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := tarsafe.ExtractInRoot(tar.NewReader(gz), dir, tarsafe.Policy{AllowSymlinks: true}); err != nil {
		return err
	}
	// safety: tar EOF precedes the gzip trailer, which must verify before publication.
	_, err = io.Copy(io.Discard, gz)
	return err
}

// ExtractArchiveStaged publishes only after extraction and gzip verification.
// Cancellation before publication preserves the previous directory.
func ExtractArchiveStaged(ctx context.Context, r io.Reader, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return tarsafe.ExtractIntoDirStaged(dir, ".depcache-restore-*", func(stage string) error {
		if err := extractArchive(r, stage); err != nil {
			return err
		}
		return ctx.Err()
	})
}
