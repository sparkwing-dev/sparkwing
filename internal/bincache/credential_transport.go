package bincache

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

func prepareCredentialTransport(env []string, username, secret string, times int) ([]string, []*os.File, func() error, error) {
	if runtime.GOOS != "windows" {
		pipe, err := credentialPipe(username, secret, times)
		if err != nil {
			return nil, nil, nil, err
		}
		return env, []*os.File{pipe}, pipe.Close, nil
	}
	// safety: the directory is private before credential bytes can reach its file.
	dir, err := fssecure.MkdirPrivateTemp("", "sparkwing-http-credential-")
	if err != nil {
		return nil, nil, nil, err
	}
	path := filepath.Join(dir, "answer")
	cleanup := func() error { return errors.Join(os.Remove(path), os.Remove(dir)) }
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fssecure.FileMode)
	if err != nil {
		return nil, nil, nil, errors.Join(err, os.Remove(dir))
	}
	if err := fssecure.SecurePrivateConfig(path); err != nil {
		return nil, nil, nil, errors.Join(err, file.Close(), cleanup())
	}
	_, writeErr := file.WriteString("username=" + username + "\npassword=" + secret + "\n")
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return nil, nil, nil, errors.Join(err, cleanup())
	}
	quoted := "'" + strings.ReplaceAll(filepath.ToSlash(path), "'", "'\"'\"'") + "'"
	helper := `!f() { test "$1" = get || return 0; cat -- ` + quoted + `; }; f`
	out := append([]string(nil), env...)
	for i, entry := range out {
		name, value, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(name, "GIT_CONFIG_VALUE_") && value == credentialHelper {
			out[i] = name + "=" + helper
		}
	}
	return out, nil, cleanup, nil
}
