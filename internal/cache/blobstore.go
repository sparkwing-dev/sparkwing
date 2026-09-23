package cache

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
)

// blobStore, when set, holds the binary, dependency-archive and artifact
// stores in a bucket instead of on the cache volume. Each team's objects
// sit under teams/<team>/ exactly as its trees do on disk, and the
// operator's under the store root. Git mirrors, workspace uploads,
// archives and the registry proxy stay on the volume, because git needs
// a real filesystem and the others are short-lived working state.
var blobStore *teamblob.Store

// hack: an indirection so a test hands New a store over an in-memory
// bucket instead of the AWS default chain.
var openBlobStore = func(ctx context.Context, raw string) (*teamblob.Store, error) {
	client, bucket, prefix, err := storeurl.OpenS3(ctx, raw)
	if err != nil {
		return nil, err
	}
	return teamblob.New(teamblob.Options{
		Bucket:           bucket,
		Prefix:           prefix,
		Client:           client,
		TeamObjectMaxAge: teamBlobMaxAge,
	})
}

// TeamBlobMaxAge is how long a team's binary, dependency archive or
// artifact lasts after it was last written. The daily reconcile deletes it
// in the listing it already makes, so the running count stays exact.
const TeamBlobMaxAge = 30 * 24 * time.Hour

// safety: the operator's own runs write under its team's namespace too, and a
// self-hosted install keeps them as long as it keeps its volume trees.
func teamBlobMaxAge(team string) time.Duration {
	if team == authwire.OperatorTeam {
		return 0
	}
	return TeamBlobMaxAge
}

// blobScratchDir stages a binary upload whose digest must be known
// before the object is written.
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

// recordBlobWrite moves the store ceiling by what a write added. Every
// object in the bucket counts, the operator's binaries included, because
// the measurement that replaces this running count is the bucket's own
// total; the operator's binaries are still never refused.
func recordBlobWrite(bytesDelta, objects int64) {
	storeCeiling.Record(bytesDelta, objects)
}

func serveBinBlob(w http.ResponseWriter, r *http.Request) {
	hash := strings.TrimPrefix(r.URL.Path, "/bin/")
	if !validBinHash.MatchString(hash) {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	team := callerFrom(r).team
	rel := "bins/" + hash
	ctx := r.Context()

	switch r.Method {
	case http.MethodHead:
		o, err := blobStore.Head(ctx, team, rel)
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
		rc, o, err := blobStore.Get(ctx, team, rel)
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
		putBinBlob(w, r, team, rel, hash)
	case http.MethodDelete:
		if err := blobStore.Delete(ctx, team, rel); err != nil {
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

// maxBinBytes caps one binary upload.
const maxBinBytes = 100 << 20

// putBinBlob stages the body on the volume to learn its digest, then
// writes it with the digest as object metadata, so a reader gets both
// from one request.
func putBinBlob(w http.ResponseWriter, r *http.Request, team, rel, hash string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBinBytes)
	release, body, ok := reserveBlobWrite(w, r, team, r.ContentLength, maxBinBytes)
	if !ok {
		return
	}
	defer release()
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
	recordBlobWrite(wr.AddedBytes, wr.AddedObjects)
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
	ctx := r.Context()

	switch r.Method {
	case http.MethodHead:
		if _, err := blobStore.Head(ctx, team, rel); err != nil {
			if !errors.Is(err, teamblob.ErrNotFound) {
				blobError(w, "head cache", err)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		rc, o, err := blobStore.Get(ctx, team, rel)
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
		n, ok := putStreamBlob(w, r, team, rel, size, "application/gzip", "cache archive", maxCacheArchiveBytes)
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

// putStreamBlob writes the request body to rel without staging it. A
// body cut off by the size cap or a hung-up client aborts the upload, so
// no partial object is ever readable.
func putStreamBlob(w http.ResponseWriter, r *http.Request, team, rel string, size int64, contentType, what string, limit int64) (int64, bool) {
	if err := blobStore.WritesPaused(); err != nil {
		blobError(w, "put "+what, err)
		return 0, false
	}
	release, body, ok := reserveBlobWrite(w, r, team, size, limit)
	if !ok {
		return 0, false
	}
	defer release()
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
	recordBlobWrite(wr.AddedBytes, wr.AddedObjects)
	return wr.Bytes, true
}

func serveArtifactsBlob(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	jobID, _, _ := strings.Cut(rest, "/")
	if jobID == "" {
		http.Error(w, "job ID required: /artifacts/{jobID}", http.StatusBadRequest)
		return
	}
	if jobID == "." || jobID == ".." || !validJobID.MatchString(jobID) {
		http.Error(w, "invalid job ID: must be 1-128 alphanumeric/dash/underscore/dot chars", http.StatusBadRequest)
		return
	}
	team := callerFrom(r).team
	switch r.Method {
	case http.MethodPost:
		artifactUploadBlob(w, r, team, jobID)
	case http.MethodGet:
		if r.URL.Query().Has("glob") {
			artifactDownloadBlob(w, r, team, jobID)
		} else {
			artifactListBlob(w, r, team, jobID)
		}
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

func artifactUploadBlob(w http.ResponseWriter, r *http.Request, team, jobID string) {
	if err := storeCeiling.Allow(); err != nil {
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
		return
	}
	artifactPath := r.URL.Query().Get("path")
	if artifactPath == "" {
		http.Error(w, "path query param required", http.StatusBadRequest)
		return
	}
	artifactPath = path.Clean(filepath.ToSlash(artifactPath))
	if strings.Contains(artifactPath, "..") || strings.HasPrefix(artifactPath, "/") || artifactPath == "." {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	for seg := range strings.SplitSeq(artifactPath, "/") {
		if strings.HasPrefix(seg, "@") || strings.HasPrefix(seg, artifactTempPrefix) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
	}
	size := int64(-1)
	if maxArtifactBytes > 0 {
		if r.ContentLength > maxArtifactBytes {
			http.Error(w, fmt.Sprintf("artifact exceeds the %d byte upload limit", maxArtifactBytes),
				http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxArtifactBytes)
	}
	if r.ContentLength >= 0 {
		size = r.ContentLength
	}
	n, ok := putStreamBlob(w, r, team, "artifacts/"+jobID+"/"+artifactPath, size, "application/octet-stream", "artifact", maxArtifactBytes)
	if !ok {
		return
	}
	// #nosec G706 -- %q escapes control characters in the caller-supplied path
	log.Printf("describe: artifact uploaded %s/%q (%d bytes)", jobID, artifactPath, n)
	w.Header().Set("Content-Type", "application/json")
	writeJSONBody(w, r, map[string]any{"path": artifactPath, "size": n})
}

func listJobBlobs(r *http.Request, team, jobID string) ([]teamblob.Object, error) {
	prefix := "artifacts/" + jobID + "/"
	objs, err := blobStore.List(r.Context(), team, prefix)
	if err != nil {
		return nil, err
	}
	for i := range objs {
		objs[i].Rel = strings.TrimPrefix(objs[i].Rel, prefix)
	}
	return objs, nil
}

func artifactListBlob(w http.ResponseWriter, r *http.Request, team, jobID string) {
	objs, err := listJobBlobs(r, team, jobID)
	if err != nil {
		blobError(w, "list artifacts", err)
		return
	}
	files := make([]string, 0, len(objs))
	for _, o := range objs {
		files = append(files, o.Rel)
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSONBody(w, r, files)
}

func artifactDownloadBlob(w http.ResponseWriter, r *http.Request, team, jobID string) {
	glob := r.URL.Query().Get("glob")
	objs, err := listJobBlobs(r, team, jobID)
	if err != nil {
		blobError(w, "list artifacts", err)
		return
	}
	if len(objs) == 0 {
		http.Error(w, "no artifacts for job "+jobID, http.StatusNotFound)
		return
	}
	var matches []teamblob.Object
	for _, o := range objs {
		if globMatches(glob, path.Base(o.Rel)) || globMatches(glob, o.Rel) {
			matches = append(matches, o)
		}
	}
	if len(matches) == 0 {
		http.Error(w, fmt.Sprintf("no artifacts matching %q for job %s", glob, jobID), http.StatusNotFound)
		return
	}
	prefix := "artifacts/" + jobID + "/"
	if len(matches) == 1 {
		name := path.Base(matches[0].Rel)
		rc, o, err := blobStore.Get(r.Context(), team, prefix+matches[0].Rel)
		if err != nil {
			blobError(w, "get artifact", err)
			return
		}
		defer rc.Close()
		// safety: an artifact is caller-supplied content, so it downloads instead of rendering in place.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", attachmentDisposition(name))
		w.Header().Set("Content-Length", strconv.FormatInt(o.Size, 10))
		if _, err := io.Copy(w, rc); err != nil {
			// #nosec G706 -- the job ID is pattern-validated
			log.Printf("warning: artifact copy for %s: %v", jobID, err)
		}
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", attachmentDisposition(jobID+".tar"))
	tw := tar.NewWriter(w)
	for _, m := range matches {
		if err := tarBlob(r.Context(), tw, team, prefix, m); err != nil {
			// #nosec G706 -- the job ID is pattern-validated
			log.Printf("warning: tar artifacts for %s: %v", jobID, err)
			return
		}
	}
	if err := tw.Close(); err != nil {
		// #nosec G706 -- the job ID is pattern-validated
		log.Printf("warning: tar artifacts for %s: %v", jobID, err)
	}
}

func tarBlob(ctx context.Context, tw *tar.Writer, team, prefix string, m teamblob.Object) error {
	rc, o, err := blobStore.Get(ctx, team, prefix+m.Rel)
	if err != nil {
		return err
	}
	defer rc.Close()
	hdr := &tar.Header{Name: m.Rel, Mode: 0o644, Size: o.Size, ModTime: m.LastModified, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(tw, rc)
	return err
}

// globMatches treats a malformed pattern as matching nothing, which is
// what the volume's walk does with it.
func globMatches(glob, name string) bool {
	ok, err := path.Match(glob, name)
	return err == nil && ok
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

// deleteTeamBlobs removes a team's whole namespace from the bucket.
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

// handleBlobUsage reports the running per-team count of what the bucket
// holds, which the controller's storage allowance reads. It lists
// nothing: the count is kept by writes and deletes and replaced by a
// listing once per --usage-reconcile. ?team= narrows it to one team.
func handleBlobUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	body := map[string]any{"enabled": blobStore != nil}
	if blobStore != nil {
		usage := blobStore.Usage()
		if team := r.URL.Query().Get("team"); team != "" {
			if !isTeamSlug(team) {
				http.Error(w, "not a team slug", http.StatusBadRequest)
				return
			}
			body["teams"] = map[string]teamblob.TeamUsage{team: usage.Team(team)}
		} else {
			teams := usage.Snapshot()
			delete(teams, "")
			body["teams"] = teams
			body["operator"] = usage.Team("")
		}
		if at := usage.ReconciledAt(); !at.IsZero() {
			body["reconciled_at"] = at.UTC().Format(time.RFC3339)
		}
		body["bucket"] = blobStore.Bucket()
		body["breaker"] = blobStore.Breaker()
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("warning: write usage: %v", err)
	}
}
