package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// safety: git mirrors, workspace uploads, archives and the registry proxy stay on the
// volume, because git needs a real filesystem and the rest is short-lived state.
var blobStore *teamblob.Store

// hack: an indirection so a test hands New a store over an in-memory
// bucket instead of the AWS default chain.
var openBlobStore = func(ctx context.Context, raw string) (*teamblob.Store, error) {
	client, bucket, prefix, err := storeurl.OpenS3(ctx, raw)
	if err != nil {
		return nil, err
	}
	return teamblob.New(teamblob.Options{Bucket: bucket, Prefix: prefix, Client: client})
}

func blobScratchDir() string { return filepath.Join(dataRoot, "tmp") }

func blobError(w http.ResponseWriter, op string, err error) {
	log.Printf("warning: blob store %s: %v", op, err)
	var paused *teamblob.SuspendedError
	switch {
	case errors.Is(err, teamblob.ErrInvalidKey):
		http.Error(w, "invalid key", http.StatusBadRequest)
	case errors.As(err, &paused):
		retry := max(int64(time.Until(paused.Until).Seconds()), 1)
		w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
		http.Error(w, "the cache's object store is paused: "+paused.Error(), http.StatusServiceUnavailable)
	default:
		http.Error(w, "blob store error", http.StatusBadGateway)
	}
}

func getScoped(r *http.Request, rel string) (io.ReadCloser, teamblob.Object, error) {
	c := callerFrom(r)
	for _, prefix := range c.prefixes() {
		rc, o, err := blobStore.Get(r.Context(), c.team, prefix+rel)
		if !errors.Is(err, teamblob.ErrNotFound) {
			return rc, o, err
		}
	}
	return nil, teamblob.Object{}, teamblob.ErrNotFound
}

func headScoped(r *http.Request, rel string) (teamblob.Object, error) {
	c := callerFrom(r)
	for _, prefix := range c.prefixes() {
		o, err := blobStore.Head(r.Context(), c.team, prefix+rel)
		if !errors.Is(err, teamblob.ErrNotFound) {
			return o, err
		}
	}
	return teamblob.Object{}, teamblob.ErrNotFound
}

// safety: a grant writes only under its run's own repository and ref, so a branch's run
// never replaces or deletes an entry its base branch's runs read.
func ownScoped(r *http.Request, rel string) string { return callerFrom(r).prefixes()[0] + rel }

func serveBinBlob(w http.ResponseWriter, r *http.Request) {
	hash, valid := binStorageKey(strings.TrimPrefix(r.URL.Path, "/bin/"))
	if !valid {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	team := callerFrom(r).team
	rel := "bins/" + hash
	ctx := r.Context()

	switch r.Method {
	case http.MethodHead:
		o, err := headScoped(r, rel)
		if errors.Is(err, teamblob.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			blobError(w, "head bin", err)
			return
		}
		if !writeBinHeaders(w, o) {
			return
		}
	case http.MethodGet:
		rc, o, err := getScoped(r, rel)
		if errors.Is(err, teamblob.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			blobError(w, "get bin", err)
			return
		}
		defer rc.Close()
		if !writeBinHeaders(w, o) {
			return
		}
		if _, err := io.Copy(w, rc); err != nil {
			// #nosec G706 -- the blob hash is pattern-validated
			log.Printf("warning: bin copy %s: %v", hash, err)
		}
	case http.MethodPut:
		if team != "" {
			if err := storeCeiling.Allow(); err != nil {
				http.Error(w, err.Error(), http.StatusInsufficientStorage)
				return
			}
		}
		if err := blobStore.WritesPaused(); err != nil {
			blobError(w, "put bin", err)
			return
		}
		putBinBlob(w, r, team, ownScoped(r, rel), hash)
	case http.MethodDelete:
		if err := blobStore.Delete(ctx, team, ownScoped(r, rel)); err != nil {
			blobError(w, "delete bin", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		http.Error(w, "GET, HEAD, PUT or DELETE only", http.StatusMethodNotAllowed)
	}
}

// safety: a binary is served only with the digest its upload recorded,
// so a client verifying the Digest header can never accept bytes the
// store did not write under that digest.
func writeBinHeaders(w http.ResponseWriter, o teamblob.Object) bool {
	digest := o.Metadata["sha256"]
	if len(digest) != 64 {
		http.Error(w, "digest unavailable", http.StatusInternalServerError)
		return false
	}
	if err := setBinDigestHeaders(w, digest); err != nil {
		http.Error(w, "digest unavailable", http.StatusInternalServerError)
		return false
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(o.Size, 10))
	return true
}

const maxBinBytes = 100 << 20

func putBinBlob(w http.ResponseWriter, r *http.Request, team, rel, hash string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBinBytes)
	counted, body, ok := reserveBlobWrite(w, r, team, r.ContentLength, maxBinBytes)
	if !ok {
		return
	}
	defer counted.finish(r.Context())
	if err := os.MkdirAll(blobScratchDir(), 0o700); err != nil {
		http.Error(w, "write error", http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(blobScratchDir(), "bin-*.tmp")
	if err != nil {
		http.Error(w, "write error", http.StatusInternalServerError)
		return
	}
	defer func() {
		_ = tmp.Close()
		// #nosec G703 -- CreateTemp supplied the private staging path
		if err := os.Remove(tmp.Name()); err != nil && !os.IsNotExist(err) {
			log.Printf("warning: remove staged binary: %v", err)
		}
	}()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), body)
	if err != nil {
		if !quotaCut(w, err, "binary") {
			http.Error(w, "read error", http.StatusBadRequest)
		}
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "write error", http.StatusInternalServerError)
		return
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	principal := writingPrincipal(r)
	wr, err := blobStore.Put(r.Context(), team, rel, tmp, teamblob.PutOptions{
		Size:        n,
		ContentType: "application/octet-stream",
		Metadata: map[string]string{
			"sha256":     digest,
			"principal":  principal,
			"written-at": time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		blobError(w, "put bin", err)
		return
	}
	counted.stored(wr.AddedBytes)
	// #nosec G706 -- the blob hash is pattern-validated
	log.Printf("bin cache: stored %s (%d bytes) sha256=%s principal=%s", hash, n, digest, principal)
	if err := setBinDigestHeaders(w, digest); err != nil {
		http.Error(w, "digest unavailable", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func serveCacheBlob(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/cache/")
	if !validCacheKey.MatchString(key) {
		http.Error(w, "invalid cache key: must be 1-128 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}
	team := callerFrom(r).team
	rel := "cache/" + key + ".tar.gz"

	switch r.Method {
	case http.MethodHead:
		if _, err := headScoped(r, rel); err != nil {
			if !errors.Is(err, teamblob.ErrNotFound) {
				blobError(w, "head cache", err)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		rc, o, err := getScoped(r, rel)
		if errors.Is(err, teamblob.ErrNotFound) {
			countCacheLookup(r, false)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if err != nil {
			blobError(w, "get cache", err)
			return
		}
		defer rc.Close()
		countCacheLookup(r, true)
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", strconv.FormatInt(o.Size, 10))
		if _, err := io.Copy(w, rc); err != nil {
			// #nosec G706 -- the cache key is pattern-validated
			log.Printf("warning: cache copy %s: %v", key, err)
		}
	case http.MethodPut:
		if err := storeCeiling.Allow(); err != nil {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
		size := int64(-1)
		if maxCacheArchiveBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, maxCacheArchiveBytes)
			if r.ContentLength > maxCacheArchiveBytes {
				http.Error(w, fmt.Sprintf("cache archive exceeds the %d byte upload limit", maxCacheArchiveBytes),
					http.StatusRequestEntityTooLarge)
				return
			}
		}
		if r.ContentLength >= 0 {
			size = r.ContentLength
		}
		n, ok := putStreamBlob(w, r, team, ownScoped(r, rel), size, "application/gzip", "cache archive", maxCacheArchiveBytes)
		if !ok {
			return
		}
		// #nosec G706 -- the cache key is pattern-validated
		log.Printf("cache store: %s (%d bytes)", key, n)
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "GET, HEAD, or PUT only", http.StatusMethodNotAllowed)
	}
}

func putStreamBlob(w http.ResponseWriter, r *http.Request, team, rel string, size int64, contentType, what string, limit int64) (int64, bool) {
	if err := blobStore.WritesPaused(); err != nil {
		blobError(w, "put "+what, err)
		return 0, false
	}
	counted, body, ok := reserveBlobWrite(w, r, team, size, limit)
	if !ok {
		return 0, false
	}
	defer counted.finish(r.Context())
	wr, err := blobStore.Put(r.Context(), team, rel, body, teamblob.PutOptions{Size: size, ContentType: contentType})
	if err != nil {
		if quotaCut(w, err, what) {
			return 0, false
		}
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, fmt.Sprintf("%s exceeds the %d byte upload limit", what, limit), http.StatusRequestEntityTooLarge)
			return 0, false
		}
		if errors.Is(err, teamblob.ErrInvalidKey) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return 0, false
		}
		blobError(w, "put "+what, err)
		return 0, false
	}
	counted.stored(wr.AddedBytes)
	return wr.Bytes, true
}

func countCacheLookup(r *http.Request, hit bool) {
	counter := gitcacheCacheMisses
	if hit {
		counter = gitcacheCacheHits
	}
	if counter != nil {
		counter.Add(r.Context(), 1, metric.WithAttributes(attribute.String("type", "dependency")))
	}
}

func deleteTeamBlobs(ctx context.Context, team string) error {
	if blobStore == nil {
		return nil
	}
	d, err := blobStore.DeleteTeam(ctx, team)
	if err == nil {
		storeCeiling.Record(-d.Bytes, -d.Objects)
		// #nosec G706 -- the team is a checked slug
		log.Printf("blob store: deleted team %s (%d objects, %d bytes)", team, d.Objects, d.Bytes)
	}
	return err
}
