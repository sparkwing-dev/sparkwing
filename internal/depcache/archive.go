package depcache

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/tarsafe"
)

// WriteArchive streams a dependency directory as a gzip-compressed tar archive.
func WriteArchive(w io.Writer, dir string) error {
	return tarsafe.WriteGzip(context.Background(), w, dir)
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
