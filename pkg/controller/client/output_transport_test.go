package client

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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
					_ = json.NewEncoder(w).Encode(store.OutputUploadGrant{URL: blobURL, UploadID: "upload", Key: "output-key"})
				case r.URL.Path == "/api/v1/runs/run/nodes/node/output-commit":
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
		if c.blobClient(target).Transport == transport {
			t.Fatalf("foreign origin %s received controller transport", target)
		}
	}
}
