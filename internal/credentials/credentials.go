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
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
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

// OpenNamed is Open for a service whose credentials are a closed set: the
// directory may hold only files named in names. A misspelled file would
// otherwise read as absent and turn its feature off, so it is refused with
// the list of names this service reads. Entries Kubernetes keeps beside a
// projected volume's files, whose names start with "..", are not credentials.
func OpenNamed(path string, names []string) (Dir, error) {
	dir, err := Open(path)
	if err != nil || path == "" {
		return dir, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return Dir{}, fmt.Errorf("--%s: %w", FlagName, err)
	}
	var unknown []string
	for _, entry := range entries {
		if name := entry.Name(); !strings.HasPrefix(name, "..") && !slices.Contains(names, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return Dir{}, fmt.Errorf("--%s %s holds %s, which this service does not read; rename or remove it (the names it reads are %s)",
			FlagName, path, strings.Join(unknown, ", "), strings.Join(names, ", "))
	}
	return dir, nil
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
	return d.read(name, fssecure.OpenCredential)
}

// ReadPrivateBytes is ReadBytes for a private key: a file other users can read
// is refused, and group read is admitted only for one of this process's groups.
func (d Dir) ReadPrivateBytes(name string) ([]byte, error) {
	return d.read(name, fssecure.OpenPrivateCredential)
}

func (d Dir) read(name string, open func(string, string) (*os.File, error)) ([]byte, error) {
	if d.path == "" {
		return nil, nil
	}
	file, err := open(d.path, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("credential %s: %w", name, err)
	}
	data, readErr := io.ReadAll(file)
	if err := errors.Join(readErr, file.Close()); err != nil {
		return nil, err
	}
	return data, nil
}

// Path reports the directory, or "" when none was named.
func (d Dir) Path() string { return d.path }
