package acceptance_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

type dataHTTPFixture struct {
	boundary  *credentialBoundary
	data      *controller.DataController
	objects   *dataObjects
	edges     *dataEdges
	events    *[]string
	reads     []blob.Digest
	charges   []blob.Blob
	chargeErr error
}

func newDataHTTPFixture() *dataHTTPFixture {
	b := newCredentialBoundary()
	p := &b.snapshot.Authorities[0].Principal
	p.Capability.Grants = append(p.Capability.Grants, capability.DataRead, capability.DataWrite)
	p.Capability.Data = dataAuthorized().Principal.Capability.Data
	a := dataAuthorized()
	a.Principal, a.Binding = *p, p.Capability.Binding
	s, _, o, e, events := dataControllerFixture(a)
	f := &dataHTTPFixture{boundary: b, data: s, objects: o, edges: e, events: events}
	s.ReadBlob = func(_ context.Context, authority blob.Authority, digest blob.Digest) (blob.Blob, error) {
		if authority != dataAuthority(a) {
			return blob.Blob{}, errors.New("private-unbound-read")
		}
		f.reads = append(f.reads, digest)
		return blob.Blob{Team: "team", Digest: digest, Size: 14}, nil
	}
	s.ChargeDownload = func(_ context.Context, _ blob.Authority, metadata blob.Blob) error {
		*events = append(*events, "charge")
		f.charges = append(f.charges, metadata)
		return f.chargeErr
	}
	return f
}

func (f *dataHTTPFixture) handler() http.Handler {
	return controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithAuthorization(consoleAuthorization(f.boundary)).WithDataController(f.data).Handler()
}

func TestDataReadHTTPDownload(t *testing.T) {
	f := newDataHTTPFixture()
	before := time.Now()
	w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/download?team=foreign", `{"digest":"`+string(dataDigest())+`"}`)
	var got controller.DataDownloadResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	if got.URL != "https://objects.example/one" || got.SHA256 != string(dataDigest()) || got.Size != 14 ||
		got.Expires.Before(before.Add(limits.DataDownloadURLTTL)) || got.Expires.After(time.Now().Add(limits.DataDownloadURLTTL)) {
		t.Fatalf("transfer=%+v", got)
	}
	if !reflect.DeepEqual(f.reads, []blob.Digest{dataDigest()}) || len(f.charges) != 1 || f.charges[0].Team != "team" || f.charges[0].Size != 14 ||
		!slices.Equal(*f.events, []string{"authorize", "charge", "sign"}) || f.objects.signed.Key != "teams/team/cas/"+string(dataDigest()) ||
		f.boundary.bindingTarget.Team != "team" || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), f.boundary.raw) {
		t.Fatalf("reads=%v charges=%v events=%v object=%+v headers=%v", f.reads, f.charges, *f.events, f.objects.signed, w.Header())
	}
}

func FuzzDataClaimQueryCannotReplaceAuthenticatedScope(f *testing.F) {
	f.Add("foreign", uint8(0))
	f.Add("job", uint8(1))
	f.Add("", uint8(2))
	f.Add("%zz", uint8(3))
	f.Fuzz(func(t *testing.T, selector string, mutation uint8) {
		fixture := newDataHTTPFixture()
		query := url.Values{"team": {selector}, "ref": {selector}, "key": {selector}}
		switch mutation % 4 {
		case 1:
			query = url.Values{"job": {selector}}
		case 2:
			query = url.Values{"job": {"job"}, "attempt": {"2"}, "generation": {"4"}}
		}
		encoded := query.Encode()
		if mutation%4 == 3 {
			encoded = "team=%zz"
		}
		w := authorityHTTP(fixture.handler(), fixture.boundary, "POST", "/api/v1/data/download?"+encoded, `{"digest":"`+string(dataDigest())+`"}`)
		if mutation%4 != 0 {
			if w.Code != 400 && w.Code != 403 || len(fixture.reads) != 0 || len(fixture.charges) != 0 || fixture.objects.signed.Key != "" {
				t.Fatalf("query selected authority: HTTP %d %s", w.Code, w.Body.String())
			}
			return
		}
		target := fixture.boundary.bindingTarget.Data
		if w.Code != 200 || target == nil || target.RunID != "run" || target.NodeID != "job" || target.Attempt != 2 || target.Generation != 4 || len(fixture.reads) != 1 || len(fixture.charges) != 1 {
			t.Fatalf("query changed authenticated scope: target=%+v HTTP %d %s", target, w.Code, w.Body.String())
		}
	})
}

func TestDataReadHTTPHasMany(t *testing.T) {
	for _, state := range []string{"mixed", "empty", "missing", "private error", "revoked", "expired budget"} {
		t.Run(state, func(t *testing.T) {
			f := newDataHTTPFixture()
			denied, missing := blob.Digest(strings.Repeat("a", 64)), blob.Digest(strings.Repeat("b", 64))
			f.edges.denyDigest = denied
			read := f.data.ReadBlob
			f.data.ReadBlob = func(ctx context.Context, a blob.Authority, d blob.Digest) (blob.Blob, error) {
				if d == missing {
					return blob.Blob{}, blob.ErrNotFound
				}
				return read(ctx, a, d)
			}
			digests := []blob.Digest{denied, missing, dataDigest(), dataDigest()}
			want := []blob.Digest{dataDigest()}
			status, code := 200, apierr.Code("")
			deadline := time.Time{}
			switch state {
			case "empty":
				digests, want = []blob.Digest{}, []blob.Digest{}
			case "missing":
				digests, want = []blob.Digest{denied, missing}, []blob.Digest{}
			case "private error":
				f.edges.err = errors.New("private-readset-database")
				status, code = 500, apierr.Internal
			case "revoked":
				f.edges.err = &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"}
				status, code = 401, apierr.Unauthenticated
			case "expired budget":
				deadline = time.Unix(1, 0)
				status, code = 408, apierr.DeadlineExceeded
			}
			body, _ := json.Marshal(map[string]any{"digests": digests, "deadline": deadline})
			w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/has-many", string(body))
			if code != "" {
				assertEngineError(t, w, status, code)
			} else {
				var got struct {
					Present []blob.Digest `json:"present"`
				}
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got.Present, want) {
					t.Fatalf("HTTP %d %s want=%v", w.Code, w.Body.String(), want)
				}
			}
			if len(f.charges) != 0 || f.objects.signed.Key != "" || slices.Contains(f.reads, denied) || strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("reads=%v charges=%v sign=%+v body=%s", f.reads, f.charges, f.objects.signed, w.Body.String())
			}
		})
	}
}

func TestDataReadHTTPRefusals(t *testing.T) {
	for _, operation := range []string{"download", "has-many"} {
		for _, state := range []string{"missing", "unknown", "legacy cache grant", "revoked", "parent revoked", "membership removed", "lease expired", "finished claim", "cancelled run", "stale generation", "wrong audience", "no grant", "machine root", "member", "foreign team", "binding refused", "no controller", "no resolver", "resolver error", "resolver revoked", "no edges"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				f := newDataHTTPFixture()
				b := f.boundary
				p := &b.snapshot.Authorities[0].Principal
				status, code := 401, apierr.Unauthenticated
				switch state {
				case "missing":
					b.raw = ""
				case "unknown":
					b.raw = "swk_" + strings.Repeat("z", 43)
				case "legacy cache grant":
					b.raw = "swcg_fixture"
				case "revoked":
					p.Capability.RevokedAt = &b.snapshot.Now
				case "parent revoked":
					b.snapshot.Authorities[1].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "membership removed":
					b.snapshot.Authorities[0].Principal.Capability.RevokedAt = &b.snapshot.Now
				case "lease expired":
					b.snapshot.Binding.LeaseExpiresAt = b.snapshot.Now
				case "finished claim":
					b.snapshot.Binding.FinishedAt = &b.snapshot.Now
				case "cancelled run":
					b.snapshot.Binding.RunCancelled = true
					status, code = 403, apierr.Forbidden
				case "stale generation":
					b.snapshot.Binding.Current.Generation++
				case "wrong audience":
					b.raw = "swn_" + strings.TrimPrefix(b.raw, "swk_")
					p.Capability.Audience = capability.NodeHost
					p.Capability.Digest = sha256.Sum256([]byte(b.raw))
					b.snapshot.Binding.ProcessAlive = true
					status, code = 403, apierr.Forbidden
				case "no grant":
					p.Capability.Grants = nil
					status, code = 403, apierr.Forbidden
				case "machine root":
					p.Capability.Unit = ""
					p.Capability.Issuance = capability.Delegate
					p.Capability.Parent = nil
					b.snapshot.Authorities = b.snapshot.Authorities[:1]
					p.Capability.Binding = capability.Binding{Kind: capability.BindingNone}
					status, code = 403, apierr.Forbidden
				case "member":
					b = authorityMember()
					f.boundary = b
					status, code = 403, apierr.Forbidden
				case "foreign team":
					p.Capability.Team = "foreign"
					status, code = 403, apierr.Forbidden
				case "binding refused":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "scope refused"}
					status, code = 403, apierr.Forbidden
				case "no resolver":
					f.data.ResolveAuthority = nil
					status, code = 500, apierr.PolicyMissing
				case "no controller":
					f.data = nil
					status, code = 500, apierr.PolicyMissing
				case "resolver error":
					f.data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) {
						return blob.Authority{}, errors.New("private-resolver")
					}
					status, code = 500, apierr.Internal
				case "resolver revoked":
					f.data.ResolveAuthority = func(context.Context, capability.Authorized) (blob.Authority, error) {
						return blob.Authority{}, &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked"}
					}
				case "no edges":
					f.data.Access = nil
					status, code = 500, apierr.PolicyMissing
				}
				body := `{"digest":"` + string(dataDigest()) + `"}`
				if operation == "has-many" {
					body = `{"digests":["` + string(dataDigest()) + `"]}`
				}
				w := authorityHTTP(f.handler(), b, "POST", "/api/v1/data/"+operation, body)
				assertEngineError(t, w, status, code)
				if len(f.reads) != 0 || len(f.charges) != 0 || f.objects.signed.Key != "" || strings.Contains(w.Body.String(), "private-") {
					t.Fatalf("reads=%v charges=%v body=%s", f.reads, f.charges, w.Body.String())
				}
			})
		}
	}
}

func TestDataReadHTTPInvalidBodiesAndMethods(t *testing.T) {
	for _, operation := range []string{"download", "has-many"} {
		valid := `{"digest":"` + string(dataDigest()) + `"}`
		if operation == "has-many" {
			valid = `{"digests":["` + string(dataDigest()) + `"]}`
		}
		for _, body := range []string{`null`, `[]`, `{}`, `{} {}`, `{"kind":"log","key":"anything"}`, `{"digest":"../foreign"}`, `{"digests":["../foreign"]}`, valid[:len(valid)-1] + `,"authority":{"team":"foreign"}}`} {
			t.Run(operation+"/"+body, func(t *testing.T) {
				f := newDataHTTPFixture()
				w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, body)
				assertEngineError(t, w, 400, apierr.BadRequest)
				if len(f.reads) != 0 || len(f.charges) != 0 || f.objects.signed.Key != "" {
					t.Fatal("invalid request reached storage")
				}
				f.boundary.raw = ""
				w = authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/"+operation, body)
				assertEngineError(t, w, 401, apierr.Unauthenticated)
			})
		}
		for _, method := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			f := newDataHTTPFixture()
			w := authorityHTTP(f.handler(), f.boundary, method, "/api/v1/data/"+operation, valid)
			assertEngineError(t, w, 405, apierr.MethodNotAllowed)
		}
	}
}

func TestDataReadHTTPDownloadFailures(t *testing.T) {
	for _, state := range []string{"unreadable", "missing", "foreign metadata", "bad digest", "negative size", "no reader", "no signer", "no meter", "meter cap", "meter error", "sign error"} {
		t.Run(state, func(t *testing.T) {
			f := newDataHTTPFixture()
			status, code := 500, apierr.Internal
			switch state {
			case "unreadable":
				f.edges.err = blob.ErrDigestNotReadable
				status, code = 403, apierr.Forbidden
			case "missing":
				f.data.ReadBlob = func(context.Context, blob.Authority, blob.Digest) (blob.Blob, error) {
					return blob.Blob{}, blob.ErrNotFound
				}
				status, code = 404, apierr.BlobMissing
			case "foreign metadata", "bad digest", "negative size":
				f.data.ReadBlob = func(context.Context, blob.Authority, blob.Digest) (blob.Blob, error) {
					metadata := blob.Blob{Team: "team", Digest: dataDigest(), Size: 14}
					if state == "foreign metadata" {
						metadata.Team = "foreign"
					}
					if state == "bad digest" {
						metadata.Digest = blob.Digest(strings.Repeat("f", 64))
					}
					if state == "negative size" {
						metadata.Size = -1
					}
					return metadata, nil
				}
			case "no reader":
				f.data.ReadBlob = nil
				code = apierr.PolicyMissing
			case "no signer":
				f.data.Objects = nil
				code = apierr.PolicyMissing
			case "no meter":
				f.data.ChargeDownload = nil
				code = apierr.PolicyMissing
			case "meter cap":
				f.chargeErr = &store.DownloadCapError{RetryAfter: time.Minute}
				status, code = 429, apierr.RateLimited
			case "meter error":
				f.chargeErr = errors.New("private-ledger")
			case "sign error":
				f.objects.signErr = errors.New("private-signer")
			}
			w := authorityHTTP(f.handler(), f.boundary, "POST", "/api/v1/data/download", `{"digest":"`+string(dataDigest())+`"}`)
			assertEngineError(t, w, status, code)
			if state != "sign error" && f.objects.signed.Key != "" {
				t.Fatal("refused download signed bytes")
			}
			if strings.Contains(w.Body.String(), "private-") {
				t.Fatalf("private error: %s", w.Body.String())
			}
			if state == "meter cap" && w.Header().Get("Retry-After") != "60" {
				t.Fatalf("retry=%v", w.Header())
			}
		})
	}
}

// safety: signing and presence reveal only generated digests admitted by the live read set.
func FuzzDataReadHTTPReadSet(f *testing.F) {
	f.Add([]byte("foreign team sibling digest"), false)
	f.Add([]byte("readable bytes"), true)
	f.Fuzz(func(t *testing.T, value []byte, readable bool) {
		sum := sha256.Sum256(value)
		digest := blob.Digest(hex.EncodeToString(sum[:]))
		fixture := newDataHTTPFixture()
		if !readable {
			fixture.edges.denyDigest = digest
		}
		h := fixture.handler()
		w := authorityHTTP(h, fixture.boundary, "POST", "/api/v1/data/has-many", `{"digests":["`+string(digest)+`","`+string(digest)+`"]}`)
		var presence struct {
			Present []blob.Digest `json:"present"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &presence) != nil || len(presence.Present) != map[bool]int{true: 1, false: 0}[readable] {
			t.Fatalf("presence HTTP %d %s", w.Code, w.Body.String())
		}
		if len(fixture.charges) != 0 {
			t.Fatal("presence charged object bytes")
		}
		w = authorityHTTP(h, fixture.boundary, "POST", "/api/v1/data/download?team=foreign&key=foreign", `{"digest":"`+string(digest)+`"}`)
		if readable {
			if w.Code != 200 || fixture.objects.signed.Key != "teams/team/cas/"+string(digest) || len(fixture.charges) != 1 {
				t.Fatalf("HTTP %d %s object=%+v", w.Code, w.Body.String(), fixture.objects.signed)
			}
		} else {
			assertEngineError(t, w, 403, apierr.Forbidden)
			if len(fixture.reads) != 0 || len(fixture.charges) != 0 || fixture.objects.signed.Key != "" {
				t.Fatal("unreadable digest reached metadata, metering or signing")
			}
		}
	})
}

func TestDataReadHTTPRepoReadersAndOwner(t *testing.T) {
	for _, class := range []string{"owner", "member repo reader", "session repo reader", "peer repo reader"} {
		t.Run(class, func(t *testing.T) {
			f := newDataHTTPFixture()
			b := f.boundary
			p := &b.snapshot.Authorities[0].Principal
			authority := dataAuthority(dataAuthorized())
			authority.Attempt.Set, authority.Upload.Set = false, false
			authority.Attempt.Value = runspec.AttemptIdentity{}
			authority.Upload.Value = blob.UploadIdentity{}
			if class == "owner" {
				p.Capability.Unit, p.Capability.Purpose = capability.UnitTrigger, capability.PurposeTrigger
				p.Capability.Binding.Kind, p.Capability.Binding.NodeID, p.Capability.Binding.Attempt = capability.BindingTrigger, "", 0
				p.Capability.Data.NodeID, p.Capability.Data.Attempt = "", 0
				b.snapshot.Binding.Current = p.Capability.Binding
			} else {
				b = authorityMember()
				f.boundary = b
				p = &b.snapshot.Authorities[0].Principal
				p.Capability.Grants = []capability.Grant{capability.DataRead}
				p.Capability.Data = &capability.DataScope{Use: capability.DataRemoteCache, RepositoryKey: "gh:12", ReadRefs: []string{"main"}, VerifiedDefaultOnly: true}
				authority.Ref.Scope.Ref = "main"
				if class == "session repo reader" {
					p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
				}
				if class == "peer repo reader" {
					p.Class, p.Authority = capability.CredentialPeer, capability.AuthorityRef{}
					p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
				}
			}
			f.data.ResolveAuthority = func(_ context.Context, a capability.Authorized) (blob.Authority, error) {
				if a.Target.Team != "team" || a.Access.Binding != capability.BindData {
					t.Fatal("unbound authority")
				}
				return authority, nil
			}
			f.data.ReadBlob = func(_ context.Context, a blob.Authority, d blob.Digest) (blob.Blob, error) {
				if a != authority {
					t.Fatal("substituted authority")
				}
				return blob.Blob{Team: "team", Digest: d, Size: 14}, nil
			}
			h := f.handler()
			for _, operation := range []string{"download", "has-many"} {
				body := `{"digest":"` + string(dataDigest()) + `"}`
				if operation == "has-many" {
					body = `{"digests":["` + string(dataDigest()) + `"]}`
				}
				w := authorityHTTP(h, b, "POST", "/api/v1/data/"+operation, body)
				if class == "peer repo reader" {
					assertEngineError(t, w, 403, apierr.Forbidden)
					continue
				}
				if w.Code != 200 {
					t.Fatalf("%s HTTP %d %s", operation, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestDataReadHTTPDiscovery(t *testing.T) {
	f := newDataHTTPFixture()
	for _, forwarded := range []bool{false, true} {
		h := controller.New(nil, slog.New(slog.NewTextHandler(io.Discard, nil))).WithAuthorization(consoleAuthorization(f.boundary)).WithDataController(f.data).Handler()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
		r.Header.Set("Authorization", "Bearer "+f.boundary.raw)
		if forwarded {
			r.Header.Set("X-Forwarded-For", "192.0.2.1")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var got controller.ServicesResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.DataDownloadURL != "/api/v1/data/download" {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
	f.data.ChargeDownload = nil
	w := discoveryRequest(f.handler(), f.boundary, "GET", "/api/v1/services")
	var got controller.ServicesResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.DataDownloadURL != "" || !got.DirectData {
		t.Fatalf("upload-only discovery HTTP %d %s", w.Code, w.Body.String())
	}
	f.data.Objects = nil
	w = discoveryRequest(f.handler(), f.boundary, "GET", "/api/v1/services")
	assertEngineError(t, w, 404, apierr.NotFound)
}

func TestDataReadHTTPChoosesCloudFrontOnlyForIngress(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("fixture-access", "fixture-secret", "")})
	objects, err := blob.NewS3(client, "fixture-bucket", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := controller.ParseCloudFrontDataConfig("cdn.example.test", "key-id", private)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := cfg.Backend(objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "X-Forwarded-For", "X-Forwarded-Host"} {
		f := newDataHTTPFixture()
		f.data.Objects = backend
		request := httptest.NewRequest(http.MethodPost, "https://controller.test/api/v1/data/download", strings.NewReader(`{"digest":"`+string(dataDigest())+`"}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+f.boundary.raw)
		if header != "" {
			request.Header.Set(header, "forwarded-client.example")
		}
		w := httptest.NewRecorder()
		f.handler().ServeHTTP(w, request)
		var response controller.DataDownloadResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("%s HTTP %d %s", header, w.Code, w.Body)
		}
		parsed, err := url.Parse(response.URL)
		if err != nil {
			t.Fatal(err)
		}
		if header == "" {
			if !strings.Contains(parsed.Host, "fixture-bucket") || parsed.Query().Get("X-Amz-Signature") == "" {
				t.Fatalf("direct URL = %s", response.URL)
			}
		} else if parsed.Host != "cdn.example.test" || parsed.Query().Get("Policy") == "" || parsed.Query().Get("Key-Pair-Id") != "key-id" {
			t.Fatalf("ingress URL = %s", response.URL)
		}
		if len(f.charges) != 1 || len(f.reads) != 1 || response.SHA256 != string(dataDigest()) || response.Size != 14 {
			t.Fatal("download lost authorized metadata or charging")
		}
	}
}
