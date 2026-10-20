package depcache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// WriteArchive streams a dependency directory as a gzip-compressed tar archive.
func WriteArchive(w io.Writer, dir string) error {
	return tarsafe.WriteGzip(context.Background(), w, dir)
}

func extractArchive(r io.Reader, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	return extractArchiveRoot(r, root)
}

func extractArchiveRoot(r io.Reader, root *os.Root) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	if err := tarsafe.ExtractIntoRoot(tar.NewReader(gz), root, tarsafe.Policy{AllowSymlinks: true}); err != nil {
		return err
	}
	// safety: tar EOF precedes the gzip trailer, which must verify before publication.
	_, err = io.Copy(io.Discard, gz)
	return err
}

func extractArchiveStaged(ctx context.Context, r io.Reader, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	parent, err := os.OpenRoot(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer parent.Close()
	return ExtractArchiveStagedRoot(ctx, r, parent, filepath.Base(dir))
}

// ExtractArchiveStagedRoot restores name inside parent, opening every path through parent.
func ExtractArchiveStagedRoot(ctx context.Context, r io.Reader, parent *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return tarsafe.ExtractIntoRootStaged(parent, name, ".depcache-restore-", func(stage *os.Root) error {
		if err := extractArchiveRoot(r, stage); err != nil {
			return err
		}
		return ctx.Err()
	})
}
