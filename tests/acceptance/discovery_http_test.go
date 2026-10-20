package acceptance_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment/deploymenttest"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

func discoveryDataController() *controller.DataController {
	return &controller.DataController{
		Objects: &dataObjects{}, Access: &dataEdges{},
		ResolveTenant:  func(context.Context, store.Team) (controller.TenantHandle, error) { return nil, nil },
		ChargeDownload: func(context.Context, blob.Authority, blob.Blob) error { return nil },
	}
}

func discoveryServer(b *credentialBoundary, configured bool) http.Handler {
	s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
	if configured {
		data := discoveryDataController()
		s.WithDashboardURL("https://dashboard.example").WithDataController(data)
	}
	return s.Handler()
}

func discoveryRequest(h http.Handler, b *credentialBoundary, method, path string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	if b.raw != "" {
		if b.snapshot.Authorities[0].Principal.Class == capability.CredentialBearer {
			r.Header.Set("Authorization", "Bearer "+b.raw)
		} else {
			r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestDiscoveryHTTPReadsConfiguredServices(t *testing.T) {
	for _, path := range []string{"/api/v1/services", "/api/v1/data/capabilities"} {
		for _, class := range []string{"member", "session", "machine", "claim", "cancelled claim", "no grants"} {
			t.Run(path+"/"+class, func(t *testing.T) {
				b := authorityMember()
				switch class {
				case "session":
					b.snapshot.Authorities[0].Principal.Class = capability.CredentialSession
					b.snapshot.Authorities[0].Principal.Authority = sessionReference(b.snapshot.Authorities[0].Principal)
				case "machine":
					b.snapshot.Authorities[0].Principal.Capability.Subject.Kind = capability.SubjectMachine
				case "claim", "cancelled claim":
					b = newCredentialBoundary()
					b.snapshot.Binding.RunCancelled = class == "cancelled claim"
				case "no grants":
					b.snapshot.Authorities[0].Principal.Capability.Grants = nil
				}
				w := discoveryRequest(discoveryServer(b, true), b, "GET", path+"?team=other")
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("HTTP %d %v: %s", w.Code, w.Header(), w.Body.String())
				}
				if path == "/api/v1/services" {
					var got controller.ServicesResponse
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if got.Dashboard != "https://dashboard.example" || !got.DirectData {
						t.Fatalf("services = %+v", got)
					}
				} else {
					var got map[string]bool
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if len(got) != 3 || !got["upload"] || got["download"] || got["source"] {
						t.Fatalf("data capabilities = %v", got)
					}
				}
			})
		}
	}
}

func TestDiscoveryHTTPRefusesInvalidAuthorityBeforeAvailability(t *testing.T) {
	for _, path := range []string{"/api/v1/services", "/api/v1/data/capabilities"} {
		for _, configured := range []bool{false, true} {
			for _, state := range []string{"absent", "malformed", "expired", "revoked parent", "membership removed", "ended claim", "stale generation", "bootstrap", "teamless", "backend error"} {
				t.Run(path+"/"+state+"/configured="+map[bool]string{false: "false", true: "true"}[configured], func(t *testing.T) {
					b := newCredentialBoundary()
					code := apierr.Unauthenticated
					switch state {
					case "absent":
						b.raw = ""
					case "malformed":
						b.raw = "bad"
					case "expired":
						b.snapshot.Authorities[0].Principal.Capability.ExpiresAt = &b.snapshot.Now
					case "revoked parent":
						b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
					case "membership removed":
						b.snapshot.Authorities[0].MembershipValid = false
					case "ended claim":
						b.snapshot.Binding.FinishedAt = &b.snapshot.Now
					case "stale generation":
						b.snapshot.Binding.Current.Generation++
					case "bootstrap":
						b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
						b.snapshot.Authorities[0].Principal.Capability.Grants = capability.BootstrapGrants()
						code = apierr.Forbidden
					case "teamless":
						b = deploymentBoundary(capability.OperatorTokens)
						code = apierr.Forbidden
					case "backend error":
						b.readErr, code = io.ErrUnexpectedEOF, apierr.Internal
					}
					w := discoveryRequest(discoveryServer(b, configured), b, "GET", path)
					assertEngineError(t, w, apierr.Status(code), code)
				})
			}
		}
	}
}

func TestDiscoveryHTTPAvailabilityAndMethods(t *testing.T) {
	for _, path := range []string{"/api/v1/services", "/api/v1/data/capabilities"} {
		b := authorityMember()
		assertEngineError(t, discoveryRequest(discoveryServer(b, false), b, "GET", path), 404, apierr.NotFound)
		assertEngineError(t, discoveryRequest(controller.New(nil, nil).Handler(), b, "GET", path), 500, apierr.PolicyMissing)
		for _, method := range []string{"HEAD", "POST", "PATCH", "DELETE", "PUT", "OPTIONS", "TRACE"} {
			w := discoveryRequest(discoveryServer(b, true), b, method, path)
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
			if w.Header().Get("Allow") != "GET" {
				t.Fatalf("Allow = %q", w.Header().Get("Allow"))
			}
		}
	}
}

func TestDiscoveryHTTPAnnouncesMultiTeamWithoutURLs(t *testing.T) {
	b := authorityMember()
	h := controller.NewDeployment(nil, deploymenttest.Team(t), slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).Handler()
	w := discoveryRequest(h, b, "GET", "/api/v1/services")
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var body controller.ServicesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.MultiTeam || body.Dashboard != "" || body.DataDownloadURL != "" || body.DirectData {
		t.Fatalf("services = %+v", body)
	}
}

func TestDiscoveryHTTPAnnouncesIndividualServices(t *testing.T) {
	for _, service := range []string{"dashboard", "uploads", "downloads"} {
		t.Run(service, func(t *testing.T) {
			b := authorityMember()
			s := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b))
			want := controller.ServicesResponse{}
			switch service {
			case "dashboard":
				s.WithDashboardURL("https://dashboard.example")
				want.Dashboard = "https://dashboard.example"
			case "uploads", "downloads":
				data := discoveryDataController()
				if service == "downloads" {
					data.ReadBlob = func(context.Context, blob.Authority, blob.Digest) (blob.Blob, error) { return blob.Blob{}, nil }
					want.DataDownloadURL = "/api/v1/data/download"
				}
				s.WithDataController(data)
				want.DirectData = true
			}
			w := discoveryRequest(s.Handler(), b, "GET", "/api/v1/services")
			if w.Code != 200 {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			var got controller.ServicesResponse
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("services = %+v, want %+v", got, want)
			}
			if service != "dashboard" {
				capabilities := discoveryRequest(s.Handler(), b, "GET", "/api/v1/data/capabilities")
				var flags map[string]bool
				if err := json.Unmarshal(capabilities.Body.Bytes(), &flags); err != nil || capabilities.Code != 200 || !flags["upload"] || flags["download"] != (service == "downloads") || flags["source"] != (service == "downloads") {
					t.Fatalf("capabilities=%s, %v", capabilities.Body.String(), err)
				}
			}
		})
	}
}

func TestDiscoveryHTTPRequiresAuthenticatedDataAdapters(t *testing.T) {
	b := authorityMember()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", "")})
	objects, err := blob.NewS3(client, "objects", "")
	if err != nil {
		t.Fatal(err)
	}
	h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(b)).WithDataObjects(objects).Handler()
	for _, path := range []string{"/api/v1/services", "/api/v1/data/capabilities"} {
		assertEngineError(t, discoveryRequest(h, b, "GET", path), 404, apierr.NotFound)
	}
}
