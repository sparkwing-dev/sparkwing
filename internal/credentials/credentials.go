// Package credentials reads a service's secrets from one directory that
// holds one file per credential under a fixed name, so a secret never
// rides in a flag value, where /proc/<pid>/cmdline shows it, or in the
// environment, which every child process inherits.
//
// Kubernetes fills the directory with one projected volume; a laptop or a
// systemd unit fills it with files. A file that is absent turns off the
// feature its credential guards, which is what an unset variable meant
// before.
package credentials

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FlagName is the flag every service names its credentials directory with.
const FlagName = "credentials-dir"

// Dir is an opened credentials directory. The zero value holds no
// credentials.
type Dir struct {
	path string
}

// Open returns the credentials directory at path. An empty path holds no
// credentials. A path that names no directory is an error, because every
// credential would read as absent and turn its feature off unannounced.
func Open(path string) (Dir, error) {
	if path == "" {
		return Dir{}, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return Dir{}, fmt.Errorf("--%s: %w", FlagName, err)
	}
	if !info.IsDir() {
		return Dir{}, fmt.Errorf("--%s: %s is not a directory", FlagName, path)
	}
	return Dir{path: path}, nil
}

// Read returns the credential stored under name with surrounding
// whitespace trimmed, so a Secret value ending in a newline matches the
// token a client sends. An absent file reads as "".
func (d Dir) Read(name string) (string, error) {
	data, err := d.ReadBytes(name)
	return strings.TrimSpace(string(data)), err
}

// ReadBytes returns the credential stored under name exactly as the file
// holds it, for a binary key whose edges may be whitespace bytes. An absent
// file reads as nil.
func (d Dir) ReadBytes(name string) ([]byte, error) {
	if d.path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(d.path, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credential %s: %w", name, err)
	}
	return data, nil
}

// Path reports the directory, or "" when none was named.
func (d Dir) Path() string { return d.path }
