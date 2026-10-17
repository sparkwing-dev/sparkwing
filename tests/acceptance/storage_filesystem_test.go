package acceptance_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

func checkStorageFilesystemCollection(t *testing.T) {
	objects, _ := storageFilesystem(t)
	transfers := blob.TransferClient{Transport: storageFilesystemHTTP(objects)}
	if err := checkBlobCollection(t.Context(), t.TempDir(), objects, transfers); err != nil {
		t.Fatal(err)
	}
}

type storageObjectHTTP struct{ http.Handler }

func (h storageObjectHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Result(), nil
}

func storageFilesystemHTTP(objects *blob.Filesystem) storageObjectHTTP {
	server := controller.New(nil, nil).WithDataObjects(objects)
	return storageObjectHTTP{server.Handler()}
}

func storageFilesystem(t *testing.T) (*blob.Filesystem, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "deployment", "nested-root")
	f, err := blob.NewFilesystem(blob.FilesystemOptions{
		Directory: dir, Endpoint: "https://controller.invalid",
		SigningKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f, dir
}

func checkStorageFilesystemCAS(t *testing.T) {
	storageProperty(t, func(seed [64]byte, length uint8) bool {
		f, dir := storageFilesystem(t)
		body := seed[:int(length)%65]
		digest := storageDigest(body)
		i, _ := storageIdentity([16]byte(seed[:16]), uint32(length))
		object := blob.Blob{Team: i.Team, Digest: digest, Size: int64(len(body))}
		pending := "teams/" + i.Team + "/pending/ticket"
		put, err := f.Sign(t.Context(), capability.ObjectToSign{
			Method: http.MethodPut, Key: pending, Size: object.Size,
			SHA256: string(digest), ExpiresAt: time.Now().Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		invalidBodies := [][]byte{append(bytes.Clone(body), 0)}
		if len(body) > 0 {
			corrupt := bytes.Clone(body)
			corrupt[0] ^= 1
			invalidBodies = append(invalidBodies, corrupt, body[:len(body)-1])
		}
		for _, invalid := range invalidBodies {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPut, put.URL, bytes.NewReader(invalid))
			r.ContentLength = -1
			storageFilesystemHTTP(f).ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				return false
			}
			if _, err := os.Stat(filepath.Join(dir, pending)); !errors.Is(err, os.ErrNotExist) {
				return false
			}
		}
		transfer := blob.TransferClient{Transport: storageFilesystemHTTP(f)}
		response, err := transfer.Transfer(t.Context(), blob.SignedTransfer{URL: put.URL, Size: object.Size, Digest: digest}, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.CommitObject(t.Context(), pending, object); err != nil {
			t.Fatal(err)
		}
		foreign := object
		foreign.Team += "-other"
		if f.CommitObject(t.Context(), pending, foreign) == nil {
			return false
		}
		if err := f.CommitObject(t.Context(), pending, object); err != nil {
			t.Fatalf("retry with this ticket's staging: %v", err)
		}
		if err := f.CommitObject(t.Context(), pending+"-missing", object); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("commit without this ticket's staging: %v", err)
		}
		key, err := blob.ObjectKey(object.Team, digest)
		if err != nil {
			t.Fatal(err)
		}
		get, err := f.Sign(t.Context(), capability.ObjectToSign{
			Method: http.MethodGet, Key: key, Size: object.Size,
			SHA256: string(digest), ExpiresAt: time.Now().Add(time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		store := blob.NewSignedStore(blob.SignedStoreOptions{
			Signer: storageDownload{transfer: blob.SignedTransfer{URL: get.URL, Size: object.Size, Digest: digest}}, HTTPClient: transfer,
		})
		r, err := store.Get(t.Context(), blob.GetRequest{Digest: digest})
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(r)
		closeErr := r.Close()
		if readErr != nil || closeErr != nil || !r.Verified() || !bytes.Equal(got, body) {
			return false
		}
		corrupt := append(bytes.Clone(body), 0)
		if len(body) > 0 {
			corrupt = bytes.Clone(body)
			corrupt[0] ^= 1
		}
		if err := os.WriteFile(filepath.Join(dir, key), corrupt, 0o600); err != nil {
			t.Fatal(err)
		}
		if !errors.Is(f.CommitObject(t.Context(), pending, object), blob.ErrIntegrity) {
			return false
		}
		if err := f.DeleteObject(t.Context(), pending); err != nil {
			t.Fatal(err)
		}
		r, err = store.Get(t.Context(), blob.GetRequest{Digest: digest})
		if err == nil {
			_, readErr = io.ReadAll(r)
			closeErr = r.Close()
			if !errors.Is(readErr, blob.ErrIntegrity) || closeErr != nil || r.Verified() {
				return false
			}
		}
		if err := f.DeleteObject(t.Context(), key); err != nil {
			t.Fatal(err)
		}
		_, err = store.Get(t.Context(), blob.GetRequest{Digest: digest})
		return errors.Is(err, blob.ErrNotFound)
	})
}

func checkStorageFilesystemSignatures(t *testing.T) {
	f, _ := storageFilesystem(t)
	storageProperty(t, func(seed [16]byte, size uint16) bool {
		object := capability.ObjectToSign{
			Method: http.MethodPut, Key: "teams/team-" + hex.EncodeToString(seed[:]) + "/pending/ticket",
			Size: int64(size), SHA256: string(storageDigest(seed[:])), ExpiresAt: time.Now().Add(time.Minute),
		}
		signed, err := f.Sign(t.Context(), object)
		if err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*capability.ObjectToSign){
			func(o *capability.ObjectToSign) { o.Method = http.MethodGet },
			func(o *capability.ObjectToSign) { o.Key = strings.Replace(o.Key, "/pending/", "-other/pending/", 1) },
			func(o *capability.ObjectToSign) { o.Key += "-other" },
			func(o *capability.ObjectToSign) { o.Size++ },
			func(o *capability.ObjectToSign) { o.SHA256 = string(storageDigest(append(seed[:], 0))) },
			func(o *capability.ObjectToSign) { o.ExpiresAt = o.ExpiresAt.Add(time.Minute) },
		} {
			changed := object
			mutate(&changed)
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(object.Method, signed.URL, http.NoBody)
			query := r.URL.Query()
			query.Set("token", base64.RawURLEncoding.EncodeToString(raw))
			r.URL.RawQuery = query.Encode()
			w := httptest.NewRecorder()
			storageFilesystemHTTP(f).ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				return false
			}
		}
		return true
	})
}

var _ http.RoundTripper = storageObjectHTTP{}
