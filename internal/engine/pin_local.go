package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func localNpmPath(spec string) string {
	if path, ok := strings.CutPrefix(spec, "file:"); ok {
		return filepath.FromSlash(path)
	}
	if !strings.Contains(spec, "://") && (strings.HasSuffix(spec, ".tgz") || strings.HasSuffix(spec, ".tar.gz")) {
		return filepath.FromSlash(spec)
	}
	return ""
}

func localNpmPackage(dir, source string) (version, input string, err error) {
	input = localNpmFile(dir, source)
	logical := source
	info, err := os.Stat(input)
	if err != nil {
		return "", "", err
	}
	directory := info.IsDir()
	if directory {
		logical = filepath.Join(logical, "package.json")
		input = filepath.Join(input, "package.json")
	}
	body, err := readPinFile(input)
	if err != nil {
		return "", "", err
	}
	if !directory {
		gz, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return "", "", err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		body = nil
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", "", err
			}
			if header.Name != "package/package.json" {
				continue
			}
			if body != nil || header.Typeflag != tar.TypeReg {
				return "", "", fmt.Errorf("local SDK archive requires one regular package/package.json")
			}
			body, err = io.ReadAll(tr)
			if err != nil {
				return "", "", err
			}
		}
	}
	var manifest struct{ Name, Version string }
	if err := decodeUniqueJSON(body, &manifest); err != nil {
		return "", "", err
	}
	if manifest.Name != typeScriptPackage || !releaseVersion("v"+manifest.Version) {
		return "", "", fmt.Errorf("local SDK package must name %s and an exact engine release version", typeScriptPackage)
	}
	return manifest.Version, logical, nil
}

func localNpmFile(dir, source string) string {
	if filepath.IsAbs(source) {
		return filepath.Clean(source)
	}
	return filepath.Join(dir, source)
}
