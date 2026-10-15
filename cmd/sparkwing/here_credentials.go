package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
)

type hereCredentials struct{ dir string }

type hereCredential struct {
	Controller string `json:"controller"`
	RunID      string `json:"run_id"`
	Bearer     string `json:"bearer"`
}

func localHereCredentials() (hereCredentials, error) {
	dir, err := fssecure.ConfigDir()
	return hereCredentials{dir: dir}, err
}

func (s hereCredentials) path(controller, run string) (string, error) {
	base, err := normalizeControllerURL(controller)
	if err != nil || !utf8.ValidString(base) || run == "" || !utf8.ValidString(run) || !filepath.IsAbs(s.dir) {
		return "", errors.New("invalid here credential binding")
	}
	body, err := json.Marshal([2]string{base, run})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	return filepath.Join(s.dir, "here", hex.EncodeToString(digest[:])+".json"), nil
}

func (s hereCredentials) load(controller, run string) (string, error) {
	path, err := s.path(controller, run)
	if err != nil {
		return "", err
	}
	f, err := fssecure.OpenPrivateConfig(path)
	if err != nil {
		return "", err
	}
	var saved hereCredential
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&saved)
	if decodeErr == nil {
		if err := decoder.Decode(new(any)); err != io.EOF {
			decodeErr = errors.New("extra credential document")
		}
	}
	closeErr := f.Close()
	base, _ := normalizeControllerURL(controller)
	if decodeErr != nil || saved.Controller != base || saved.RunID != run || saved.Bearer == "" {
		return "", errors.New("invalid retained here credential")
	}
	return saved.Bearer, closeErr
}

func (s hereCredentials) save(controller, run, bearer string) error {
	path, err := s.path(controller, run)
	if err != nil {
		return err
	}
	if bearer == "" || !utf8.ValidString(bearer) {
		return errors.New("here handoff has no bearer")
	}
	if err := fssecure.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := fssecure.SecurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := fssecure.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if errors.Is(err, os.ErrExist) {
		saved, loadErr := s.load(controller, run)
		if loadErr != nil {
			return loadErr
		}
		if saved != bearer {
			return errors.New("here handoff conflicts with retained authority")
		}
		return nil
	}
	if err != nil {
		return err
	}
	base, _ := normalizeControllerURL(controller)
	err = errors.Join(json.NewEncoder(f).Encode(hereCredential{Controller: base, RunID: run, Bearer: bearer}), f.Close())
	if err != nil {
		return errors.Join(err, os.Remove(path))
	}
	return nil
}
