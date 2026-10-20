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
