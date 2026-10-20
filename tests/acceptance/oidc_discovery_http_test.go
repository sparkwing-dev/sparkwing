package acceptance_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
)

func TestOIDCDiscoveryHTTPPublicPolicy(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	issuer, err := oidcissuer.New("https://issuer.example", private, nil, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithOIDCIssuer(issuer).Handler()
	for _, path := range []string{oidcissuer.DiscoveryPath, oidcissuer.JWKSPath} {
		for _, auth := range []string{"", "Bearer malformed", "Session untrusted"} {
			t.Run(path+"/"+auth, func(t *testing.T) {
				r := httptest.NewRequest("GET", "http://forged.example"+path, nil)
				r.Header.Set("Authorization", auth)
				r.AddCookie(&http.Cookie{Name: "__Host-sw_session", Value: "untrusted"})
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
				}
				if w.Header().Get("Cache-Control") != "public, max-age=3600" || w.Header().Get("Pragma") != "" {
					t.Fatalf("public cache headers = %v", w.Header())
				}
				if path == oidcissuer.DiscoveryPath {
					var got struct {
						Issuer string `json:"issuer"`
						JWKS   string `json:"jwks_uri"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.Issuer != "https://issuer.example" || got.JWKS != "https://issuer.example"+oidcissuer.JWKSPath {
						t.Fatalf("discovery = %s", w.Body.String())
					}
				} else {
					var got struct {
						Keys []map[string]string `json:"keys"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if len(got.Keys) != 1 || got.Keys[0]["kid"] != oidcissuer.Thumbprint(&key.PublicKey) || got.Keys[0]["kty"] != "RSA" {
						t.Fatalf("public keys = %s", w.Body.String())
					}
					for _, field := range []string{"d", "p", "q", "dp", "dq", "qi", "oth"} {
						if _, exists := got.Keys[0][field]; exists {
							t.Fatalf("JWKS contains private field %s", field)
						}
					}
				}
			})
		}
	}
}

func TestOIDCDiscoveryHTTPDisabledAndMethods(t *testing.T) {
	h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	for _, path := range []string{oidcissuer.DiscoveryPath, oidcissuer.JWKSPath} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		assertEngineError(t, w, 404, apierr.NotFound)
		for _, method := range []string{"HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "TRACE"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
			if w.Header().Get("Allow") != "GET" {
				t.Fatalf("Allow = %q", w.Header().Get("Allow"))
			}
		}
	}
}
