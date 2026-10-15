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

func writeArchive(w io.Writer, dir string) error {
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
	return tarsafe.ExtractInRoot(tar.NewReader(gz), dir, tarsafe.Policy{AllowSymlinks: true})
}

func extractArchiveStaged(r io.Reader, dir string) error {
	return tarsafe.ExtractIntoDirStaged(dir, ".depcache-restore-*", func(stage string) error {
		return extractArchive(r, stage)
	})
}
