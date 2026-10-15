package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestOutputDownloadRejectsOversizedVerifiedBytes(t *testing.T) {
	for _, size := range []int64{limits.NodeOutputBytes, limits.NodeOutputBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), int(size))
			sum := sha256.Sum256(body)
			transport := dataTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})
			c := NewWithToken("https://controller.test", &http.Client{Transport: transport}, "credential")
			value, err := c.fetchOutput(t.Context(), store.OutputReadGrant{URL: "/blob", SHA256: hex.EncodeToString(sum[:]), Size: size})
			if size > limits.NodeOutputBytes {
				if len(value) != 0 || !errors.Is(err, store.ErrOutputCorrupt) {
					t.Fatalf("value=%d bytes, err=%v", len(value), err)
				}
			} else if err != nil || !bytes.Equal(value, body) {
				t.Fatalf("value=%d bytes, err=%v", len(value), err)
			}
		})
	}
}

func TestSignedOutputUploadUsesControllerSocketOnlyForSameOrigin(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-origin", true: "same-host-other-port"}[foreign], func(t *testing.T) {
			var uploads atomic.Int32
			receive := func(w http.ResponseWriter, r *http.Request) {
				uploads.Add(1)
				if r.Header.Get("Authorization") != "" {
					t.Error("signed upload carried bearer authority")
				}
				for _, h := range []string{store.ClaimHolderHeader, store.ClaimMembershipHeader, store.ClaimReservationHeader, store.ClaimGenerationHeader} {
					if r.Header.Get(h) != "" {
						t.Errorf("signed upload carried %s", h)
					}
				}
				b, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if string(b) != "output" {
					t.Errorf("upload body = %q", b)
				}
				w.WriteHeader(http.StatusNoContent)
			}
			baseURL := "http://sparkwing-api"
			if foreign {
				baseURL = "http://127.0.0.1:1"
			}
			blobURL := baseURL + "/blob"
			if foreign {
				server := httptest.NewServer(http.HandlerFunc(receive))
				defer server.Close()
				blobURL = server.URL + "/blob"
			}
			listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "api.sock"))
			if err != nil {
				t.Skipf("unix socket unavailable: %v", err)
			}
			mux := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/blob":
					if foreign {
						t.Error("foreign origin was sent through controller socket")
					}
					receive(w, r)
				case r.URL.Path == "/api/v1/runs/run/nodes/node/output-upload":
					if r.Header.Get("Authorization") != "Bearer api-token" {
						t.Error("grant request missing bearer")
					}
					_ = json.NewEncoder(w).Encode(store.OutputUploadGrant{URL: blobURL, UploadID: "upload", Key: "output-key"})
				case r.URL.Path == "/api/v1/runs/run/nodes/node/output-commit":
					if r.Header.Get("Authorization") != "Bearer api-token" {
						t.Error("commit request missing bearer")
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			server := httptest.NewUnstartedServer(mux)
			server.Listener = listener
			server.Start()
			defer server.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", listener.Addr().String())
			}}
			defer transport.CloseIdleConnections()
			c := NewWithToken(baseURL, &http.Client{Transport: transport}, "api-token")
			ctx, cancel := context.WithTimeout(store.WithNodeClaimFence(t.Context(), store.NodeClaimFence{HolderID: "holder", MembershipID: "membership", ReservationID: "reservation", ClaimGeneration: 2}), time.Second)
			defer cancel()
			if _, err := c.UploadNodeOutput(ctx, "run", "node", []byte("output")); err != nil {
				t.Fatal(err)
			}
			if uploads.Load() != 1 {
				t.Fatalf("uploads=%d", uploads.Load())
			}
		})
	}
}

func TestSignedOutputTransportRequiresControllerSchemeAndPort(t *testing.T) {
	transport := &http.Transport{}
	c := NewWithToken("http://sparkwing-api", &http.Client{Transport: transport}, "api-token")
	for _, target := range []string{"https://sparkwing-api/blob", "http://sparkwing-api:81/blob"} {
		if c.blobTransport(target) == transport {
			t.Fatalf("foreign origin %s received controller transport", target)
		}
	}
}

func FuzzOutputTransferKeepsControllerCredentialsPrivate(f *testing.F) {
	f.Add([]byte("output"), uint32(1))
	f.Fuzz(func(t *testing.T, output []byte, generation uint32) {
		if len(output) == 0 || int64(len(output)) > limits.NodeOutputBytes {
			return
		}
		sum := sha256.Sum256(output)
		token := hex.EncodeToString(sum[:])
		var uploads, commits int
		transport := dataTransport(func(r *http.Request) (*http.Response, error) {
			body := ""
			switch r.URL.Path {
			case "/api/v1/runs/run/nodes/node/output-upload":
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("upload reservation lost controller authority")
				}
				body = `{"url":"/blob","upload_id":"upload","key":"output"}`
			case "/blob":
				uploads++
				for _, header := range []string{"Authorization", store.ClaimHolderHeader, store.ClaimMembershipHeader, store.ClaimReservationHeader, store.ClaimGenerationHeader} {
					if r.Header.Get(header) != "" {
						t.Fatalf("signed transfer carried %s", header)
					}
				}
				got, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(got, output) || r.ContentLength != int64(len(output)) {
					t.Fatalf("signed upload changed bytes: %v", err)
				}
			case "/api/v1/runs/run/nodes/node/output-commit":
				commits++
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Fatal("commit lost controller authority")
				}
			default:
				t.Fatalf("unexpected output path %s", r.URL.Path)
			}
			status := http.StatusOK
			if r.URL.Path != "/api/v1/runs/run/nodes/node/output-upload" {
				status = http.StatusNoContent
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewBufferString(body)), Header: make(http.Header), Request: r}, nil
		})
		c := NewWithToken("https://controller.test", &http.Client{Transport: transport}, token)
		ctx := store.WithNodeClaimFence(t.Context(), store.NodeClaimFence{HolderID: token, MembershipID: token, ReservationID: token, ClaimGeneration: int64(generation) + 1})
		ref, err := c.UploadNodeOutput(ctx, "run", "node", output)
		if err != nil || ref == nil || ref.SHA256 != token || uploads != 1 || commits != 1 {
			t.Fatalf("upload result=%+v, uploads=%d, commits=%d, err=%v", ref, uploads, commits, err)
		}
	})
}
