package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestDataStorageSignedFilesystemRoutes(t *testing.T) {
	dir := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: dir, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	server := New(nil, nil).WithDataObjects(objects)
	router := server.Handler()
	property := func(payload [32]byte, tenant uint16) bool {
		team := fmt.Sprintf("tenant-%d", tenant)
		digest := blob.Digest(fmt.Sprintf("%x", sha256.Sum256(payload[:])))
		key := "teams/" + team + "/pending/" + string(digest)
		put, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodPut, Key: key, Size: 32, SHA256: string(digest), ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Log(err)
			return false
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPut, put.URL, bytes.NewReader(payload[:])))
		if w.Code != http.StatusCreated {
			t.Logf("PUT: %d %s", w.Code, w.Body.String())
			return false
		}
		object := blob.Blob{Team: team, Digest: digest, Size: 32}
		if err := objects.CommitObject(t.Context(), key, object); err != nil {
			t.Log(err)
			return false
		}
		final, err := blob.ObjectKey(team, digest)
		if err != nil {
			t.Log(err)
			return false
		}
		get, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: http.MethodGet, Key: final, Size: 32, SHA256: string(digest), ExpiresAt: time.Now().Add(30 * time.Second)})
		if err != nil {
			t.Log(err)
			return false
		}
		w = httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, get.URL, nil))
		if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), payload[:]) {
			t.Logf("GET: %d %s", w.Code, w.Body.String())
			return false
		}
		for _, transfer := range []struct{ method, url string }{{http.MethodPut, put.URL}, {http.MethodGet, get.URL}} {
			for _, path := range []string{"/api/v1/data/uploads/other", "/api/v1/data/objects/teams/other/cas/" + string(digest), "/api/v1/log-segments/logs/other"} {
				r := httptest.NewRequest(transfer.method, transfer.url, nil)
				r.URL.Path, r.URL.RawPath = path, ""
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code >= 200 && w.Code < 300 {
					t.Logf("substituted transfer succeeded: %s", path)
					return false
				}
				var envelope apierr.Envelope
				if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil || envelope.Error == nil {
					t.Logf("noncanonical transfer refusal: %v %s", err, w.Body)
					return false
				}
				if envelope.Error.Code == apierr.Forbidden && envelope.Error.Details["reason"] == nil {
					t.Log("transfer refusal lacks reason")
					return false
				}
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64, Rand: rand.New(rand.NewSource(7))}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeBindingForbiddenReasonProperty(t *testing.T) {
	_, handler, root := engineClaimHTTPFixture(t, []capability.Grant{capability.NodesClaim})
	response := engineClaimHTTP(t, handler, root, "POST", "/api/v1/runners/claim", `{"runner_version":"v1.2.3","kinds":["node"]}`, 200)
	var award controllerwire.ClaimResponse
	if err := json.Unmarshal(response.Body.Bytes(), &award); err != nil || award.Claim == nil {
		t.Fatalf("claim: %v %s", err, response.Body)
	}
	property := func(selector [16]byte) bool {
		path := fmt.Sprintf("/api/v1/pipelines/foreign-%x/sizing", selector)
		response := engineClaimHTTP(t, handler, award.Claim.Capability, "GET", path, "", 403)
		var envelope apierr.Envelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || envelope.Error == nil {
			t.Logf("invalid error: %v %s", err, response.Body)
			return false
		}
		return envelope.Error.Code == apierr.Forbidden && envelope.Error.Details["reason"] == string(apierr.ReasonBinding)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 32}); err != nil {
		t.Fatal(err)
	}
}

func TestSignedFilesystemCanonicalTransportErrors(t *testing.T) {
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: t.TempDir(), Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	handler := New(nil, nil).WithDataObjects(objects).Handler()
	for _, tc := range []struct {
		method, path string
		status       int
		code         apierr.Code
		allow        string
	}{
		{"GET", "/api/v1/data/objects/teams/a/cas/digest", 403, apierr.Forbidden, ""},
		{"HEAD", "/api/v1/data/objects/teams/a/cas/digest", 405, apierr.MethodNotAllowed, "GET"},
		{"PUT", "/api/v1/data/uploads/upload", 403, apierr.Forbidden, ""},
		{"PUT", "/api/v1/log-segments/logs/a/run/job/segment", 403, apierr.Forbidden, ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			var envelope apierr.Envelope
			if w.Code != tc.status || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error == nil || envelope.Error.Code != tc.code || w.Header().Get("Allow") != tc.allow {
				t.Fatalf("response: %d %v %s", w.Code, w.Header(), w.Body)
			}
			if tc.code == apierr.Forbidden && envelope.Error.Details["reason"] == nil {
				t.Fatal("missing forbidden reason")
			}
		})
	}
}

func TestSignedFilesystemCanonicalObjectErrors(t *testing.T) {
	directory := t.TempDir()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: directory, Endpoint: "https://controller.invalid", SigningKey: bytes.Repeat([]byte{42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := objects.Close(); err != nil {
			t.Error(err)
		}
	})
	handler := New(nil, nil).WithDataObjects(objects).Handler()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("x")))
	for _, tc := range []struct {
		name, method, key, body string
		code                    apierr.Code
		prepare                 bool
	}{
		{"upload_integrity", "PUT", "teams/a/pending/" + digest, "xx", apierr.BadRequest, false},
		{"missing", "GET", "teams/a/cas/" + digest, "", apierr.NotFound, false},
		{"read_integrity", "GET", "teams/a/cas/" + digest, "", apierr.Conflict, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.prepare {
				file := filepath.Join(directory, tc.key)
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("xx"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			signed, err := objects.Sign(t.Context(), capability.ObjectToSign{Method: tc.method, Key: tc.key, Size: 1, SHA256: digest, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(tc.method, signed.URL, bytes.NewBufferString(tc.body)))
			var envelope apierr.Envelope
			if w.Code != apierr.Status(tc.code) || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error == nil || envelope.Error.Code != tc.code {
				t.Fatalf("response: %d %v %s", w.Code, w.Header(), w.Body)
			}
		})
	}
}
