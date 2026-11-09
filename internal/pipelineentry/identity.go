package pipelineentry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Identity fingerprints the resolved runtime, entry and installed pipeline files.
func (c Command) Identity(directory, runtimeVersion string) (string, error) {
	if len(c) != 2 {
		return "", errors.New("prepared entry needs a runtime and script")
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00", runtimeVersion)
	runtime, err := os.Open(c[0])
	if err != nil {
		return "", err
	}
	_, err = io.Copy(sum, runtime)
	closeErr := runtime.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	entry, err := filepath.Rel(directory, c[1])
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(c[1])
	if err != nil {
		return "", err
	}
	entryDigest := sha256.Sum256(body)
	fmt.Fprintf(sum, "\x00entry:%s\x00%x\x00", filepath.ToSlash(entry), entryDigest)
	visited := map[string]bool{}
	var walk func(string, string) error
	walk = func(root, prefix string) error {
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			return err
		}
		if visited[root] {
			return nil
		}
		visited[root] = true
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			relative = filepath.Join(prefix, relative)
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if info.IsDir() {
				fmt.Fprintf(sum, "directory:%s\x00", filepath.ToSlash(relative))
				return walk(path, relative)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(body)
			fmt.Fprintf(sum, "%s\x00%x\x00", filepath.ToSlash(relative), digest)
			return nil
		})
	}
	err = walk(directory, "")
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
