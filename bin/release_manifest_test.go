package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestReleaseManifestCanBePublishedByDigestWithoutATag(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("Docker CLI is not installed")
	}
	if err := exec.Command(docker, "buildx", "version").Run(); err != nil {
		t.Skip("Buildx is not installed")
	}
	const media = "application/vnd.oci.image.index.v1+json"
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a","size":2},"layers":[]}`)
	manifestDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(manifest))
	bodies := map[string][]byte{manifestDigest: manifest}
	var sources []string
	for _, arch := range []string{"amd64", "arm64"} {
		body := fmt.Appendf(nil, `{"schemaVersion":2,"mediaType":%q,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":%d,"platform":{"os":"linux","architecture":%q}}]}`, media, manifestDigest, len(manifest), arch)
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		bodies[digest] = body
		sources = append(sources, digest)
	}
	var mu sync.Mutex
	var writes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v2/fixture/blobs/sha256:") {
			w.Header().Set("Content-Length", "2")
			w.Header().Set("Docker-Content-Digest", strings.TrimPrefix(r.URL.Path, "/v2/fixture/blobs/"))
			if r.Method != http.MethodHead {
				if _, err := io.WriteString(w, "{}"); err != nil {
					t.Log(err)
				}
			}
			return
		}
		const prefix = "/v2/fixture/manifests/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		ref := strings.TrimPrefix(r.URL.Path, prefix)
		if r.Method == http.MethodPut {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			bodies[ref] = body
			writes = append(writes, ref)
			w.Header().Set("Docker-Content-Digest", ref)
			w.WriteHeader(http.StatusCreated)
			return
		}
		body, ok := bodies[ref]
		if !ok {
			http.NotFound(w, r)
			return
		}
		var kind struct {
			MediaType string `json:"mediaType"`
		}
		if err := json.Unmarshal(body, &kind); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", kind.MediaType)
		w.Header().Set("Docker-Content-Digest", ref)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if r.Method != http.MethodHead {
			if _, err := w.Write(body); err != nil {
				t.Log(err)
			}
		}
	}))
	defer server.Close()
	image := strings.TrimPrefix(server.URL, "http://") + "/fixture"
	args := []string{"buildx", "imagetools", "create", "--dry-run", image + "@" + sources[0], image + "@" + sources[1]}
	body, err := exec.Command(docker, args...).Output()
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.TrimSuffix(string(body), "\n"))
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(body))
	args = []string{"buildx", "imagetools", "create", "--tag", image + "@" + digest, image + "@" + sources[0], image + "@" + sources[1]}
	if out, err := exec.Command(docker, args...).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(writes) != 1 || writes[0] != digest {
		t.Fatalf("registry writes=%v want only %s", writes, digest)
	}
	if string(bodies[digest]) != string(body) {
		t.Fatal("pushed manifest differs from the manifest hashed before publication")
	}
}
