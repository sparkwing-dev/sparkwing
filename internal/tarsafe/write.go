package tarsafe

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// WriteGzip captures a directory without reading through escaping symlinks.
func WriteGzip(ctx context.Context, dst io.Writer, dir string) (err error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	gzipWriter := gzip.NewWriter(dst)
	tarWriter := tar.NewWriter(gzipWriter)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := context.Cause(ctx); err != nil {
			return err
		}
		if walkErr != nil || name == "." {
			return walkErr
		}
		return writeEntry(ctx, tarWriter, root, name, entry)
	})
	return errors.Join(err, tarWriter.Close(), gzipWriter.Close())
}

func writeEntry(ctx context.Context, dst *tar.Writer, root *os.Root, name string, entry fs.DirEntry) (err error) {
	info, err := entry.Info()
	if err != nil {
		return err
	}
	link := ""
	var file *os.File
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		link, err = root.Readlink(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		if err := symlinkStaysInside(root, filepath.FromSlash(name), link); err != nil {
			return err
		}
	case info.Mode().IsRegular():
		file, err = root.Open(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		info, err = file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("cache entry changed file type")
		}
	case !info.IsDir():
		return nil
	}
	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return err
	}
	header.Name = name
	header.Mode = int64(info.Mode().Perm())
	if err := dst.WriteHeader(header); err != nil {
		return err
	}
	if file != nil {
		_, err = io.Copy(dst, contextReader{ctx, file})
	}
	return err
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r contextReader) Read(dst []byte) (int, error) {
	if err := context.Cause(r.ctx); err != nil {
		return 0, err
	}
	return r.src.Read(dst)
}
