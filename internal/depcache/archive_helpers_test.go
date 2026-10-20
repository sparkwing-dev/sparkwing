package depcache

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

func writeArchive(w io.Writer, dir string) error {
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
