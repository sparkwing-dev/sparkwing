package acceptance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type credentialMaintenance struct {
	team       store.Team
	rolledBack []store.Secret
	secrets    []store.Secret
	git        []store.GitCredential
	audits     []capability.SourceReleaseAudit
	err        error
	resolveErr error
	missing    bool
	resolves   int
	calls      int
	actor      capability.Authorized
}

func (f *credentialMaintenance) Team() store.Team { return f.team }

func (f *credentialMaintenance) RotateCredentials(_ context.Context, a capability.Authorized, secret func(store.Secret) (string, bool, error), git func(store.GitCredential) (string, error)) (store.CredentialRotation, error) {
	f.calls++
	f.actor = a
	if f.err != nil {
		return store.CredentialRotation{}, f.err
	}
	for _, row := range f.rolledBack {
		if _, _, err := secret(row); err != nil {
			return store.CredentialRotation{}, err
		}
	}
	result := store.CredentialRotation{Skipped: []store.SecretRotationSkip{}}
	rows := append([]store.Secret(nil), f.secrets...)
	creds := append([]store.GitCredential(nil), f.git...)
	for i := range rows {
		value, skip, err := secret(rows[i])
		if err != nil {
			return store.CredentialRotation{}, err
		}
		if skip {
			result.Skipped = append(result.Skipped, store.SecretRotationSkip{Name: rows[i].Name, Pipeline: rows[i].Pipeline})
			continue
		}
		rows[i].Value = value
		result.Rotated++
	}
	for i := range creds {
		value, err := git(creds[i])
		if err != nil {
			return store.CredentialRotation{}, err
		}
		creds[i].Secret = value
	}
	f.secrets, f.git = rows, creds
	result.GitCredentials = len(creds)
	return result, nil
}

func (f *credentialMaintenance) SourceReleaseAudits(_ context.Context, limit int) ([]capability.SourceReleaseAudit, error) {
	f.calls++
	if limit != 200 {
		return nil, errors.New("invalid audit limit")
	}
	return f.audits, f.err
}

func maintenanceServer(t testing.TB, b *credentialBoundary, f *credentialMaintenance) *controller.Server {
	t.Helper()
	s := mutationServerConfig(t, nil, nil, b)
	resolve := func(team store.Team) {
		f.resolves++
		if team != "team" {
			t.Fatalf("resolved tenant %q", team)
		}
	}
	s.WithCredentialRotator(func(_ context.Context, team store.Team) (controller.CredentialRotator, error) {
		resolve(team)
		if f.missing {
			return nil, f.resolveErr
		}
		return f, f.resolveErr
	})
	s.WithSourceAuditReader(func(_ context.Context, team store.Team) (controller.SourceAuditReader, error) {
		resolve(team)
		if f.missing {
			return nil, f.resolveErr
		}
		return f, f.resolveErr
	})
	return s
}

func TestCredentialMaintenanceHTTPRequiresLiveAuthority(t *testing.T) {
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/secrets/rotate"},
		{"GET", "/api/v1/team/git-credentials/releases"},
	} {
		for _, identity := range []string{"anonymous", "revoked", "expired", "membership", "grant", "machine", "work", "trigger", "bootstrap", "binding"} {
			t.Run(route.path+"/"+identity, func(t *testing.T) {
				b := authorityMember()
				p := &b.snapshot.Authorities[0].Principal
				code := apierr.Forbidden
				switch identity {
				case "anonymous":
					b.raw, code = "", apierr.Unauthenticated
				case "revoked":
					p.Capability.RevokedAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "expired":
					p.Capability.ExpiresAt, code = &b.snapshot.Now, apierr.Unauthenticated
				case "membership":
					b.snapshot.Authorities[0].MembershipValid, code = false, apierr.Unauthenticated
				case "grant":
					p.Capability.Grants = nil
				case "machine":
					p.Capability.Subject.Kind = capability.SubjectMachine
				case "work", "trigger", "bootstrap":
					b = newCredentialBoundary()
					if identity == "trigger" {
						b = ownerBoundary()
					}
					b.snapshot.Authorities[0].Principal.Capability.Grants = p.Capability.Grants
					if identity == "bootstrap" {
						b.snapshot.Authorities[0].Principal.Capability.Delivery = capability.DeliveryBootstrap
					}
				case "binding":
					b.bindingErr = &apierr.Error{Code: apierr.Forbidden, Message: "outside team"}
				}
				w := authorityHTTP(mutationServer(t, nil, nil, b), b, route.method, route.path, "")
				assertEngineError(t, w, apierr.Status(code), code)
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("credential response is cacheable")
				}
			})
		}
	}
}

func maintenanceCiphers(t testing.TB) (*secrets.Cipher, *secrets.Cipher, *secrets.Cipher) {
	t.Helper()
	oldKey, newKey := bytes.Repeat([]byte{36}, secrets.KeySize), bytes.Repeat([]byte{37}, secrets.KeySize)
	old, err := secrets.NewCipher(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	current, err := secrets.NewCipher(newKey)
	if err != nil {
		t.Fatal(err)
	}
	rotating, err := secrets.NewCipherWithPrevious(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	return old, current, rotating
}

func TestCredentialRotationHTTPPreservesTenantAndRowBindings(t *testing.T) {
	old, current, rotating := maintenanceCiphers(t)
	property := func(value, pipeline, selector string, masked, shared bool) bool {
		digest := sha256.Sum256([]byte(value))
		plain := "private-" + hex.EncodeToString(digest[:])
		row := store.Secret{Name: "TOKEN", Pipeline: pipeline, Masked: masked, Shared: shared, Principal: "creator", CreatedAt: time.Unix(10, 0), UpdatedAt: time.Unix(20, 0)}
		var err error
		row.Value, err = old.SealBound(secrets.PurposeSecret, "team", row.Name, pipeline, shared, masked, plain)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := current.OpenBound(secrets.PurposeSecret, "team", row.Name, pipeline, shared, masked, row.Value); err == nil {
			return false
		}
		sealed, err := old.SealBound(secrets.PurposeGitCredential, "team", "git-credential:git.invalid", "git-credential", false, true, plain)
		if err != nil {
			t.Fatal(err)
		}
		git := store.GitCredential{ID: "credential", Team: "team", Host: "git.invalid", Kind: store.GitCredentialHTTPS, Secret: sealed, CreatedBy: "owner"}
		b := authorityMember()
		f := &credentialMaintenance{team: "team", secrets: []store.Secret{row}, git: []store.GitCredential{git}}
		query := url.Values{"team": {selector}, "pipeline": {selector}, "run": {selector}}
		w := authorityHTTP(maintenanceServer(t, b, f).WithSecretsCipher(rotating).Handler(), b, "POST", "/api/v1/secrets/rotate?"+query.Encode(), "")
		var result struct {
			Rotated        int
			GitCredentials int `json:"git_credentials"`
			Skipped        []json.RawMessage
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Rotated != 1 || result.GitCredentials != 1 || result.Skipped == nil || len(result.Skipped) != 0 || strings.Contains(w.Body.String(), plain) || w.Header().Get("Cache-Control") != "no-store" || f.calls != 1 || f.actor.Principal.Authority != b.snapshot.Authorities[0].Principal.Authority || f.actor.Target.Team != "team" {
			return false
		}
		if opened, err := current.OpenBound(secrets.PurposeSecret, "team", row.Name, pipeline, shared, masked, f.secrets[0].Value); err != nil || opened != plain {
			return false
		}
		if _, err := current.OpenBound(secrets.PurposeSecret, "foreign", row.Name, pipeline, shared, masked, f.secrets[0].Value); err == nil {
			return false
		}
		if opened, err := current.OpenBound(secrets.PurposeGitCredential, "team", "git-credential:git.invalid", "git-credential", false, true, f.git[0].Secret); err != nil || opened != plain {
			return false
		}
		f.secrets[0].Value, f.git[0].Secret = row.Value, git.Secret
		return reflect.DeepEqual(f.secrets[0], row) && reflect.DeepEqual(f.git[0], git)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialRotationHTTPSkipsUnreadableSecrets(t *testing.T) {
	old, current, rotating := maintenanceCiphers(t)
	stray, err := secrets.NewCipher(bytes.Repeat([]byte{38}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	good, err := old.SealBound(secrets.PurposeSecret, "team", "GOOD", "", false, true, "readable")
	if err != nil {
		t.Fatal(err)
	}
	lost, err := stray.SealBound(secrets.PurposeSecret, "team", "LOST", "build", false, true, "private-lost")
	if err != nil {
		t.Fatal(err)
	}
	wrongBinding, err := old.SealBound(secrets.PurposeSecret, "foreign", "FOREIGN", "build", false, true, "foreign-private")
	if err != nil {
		t.Fatal(err)
	}
	f := &credentialMaintenance{team: "team", secrets: []store.Secret{
		{Name: "GOOD", Value: good, Masked: true},
		{Name: "LOST", Pipeline: "build", Value: lost, Masked: true},
		{Name: "FOREIGN", Pipeline: "build", Value: wrongBinding, Masked: true},
		{Name: "IMPOSTOR", Value: "enc:v1:not-really-an-envelope", Masked: true},
		{Name: "PLAIN", Value: "private-plaintext"},
	}}
	b := authorityMember()
	w := authorityHTTP(maintenanceServer(t, b, f).WithSecretsCipher(rotating).Handler(), b, "POST", "/api/v1/secrets/rotate", "")
	var body struct {
		Rotated int
		Skipped []struct{ Name, Pipeline string }
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Rotated != 1 || len(body.Skipped) != 4 || body.Skipped[0].Name != "LOST" || body.Skipped[0].Pipeline != "build" || body.Skipped[1].Name != "FOREIGN" || body.Skipped[2].Name != "IMPOSTOR" || body.Skipped[3].Name != "PLAIN" || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	if value, err := current.OpenBound(secrets.PurposeSecret, "team", "GOOD", "", false, true, f.secrets[0].Value); err != nil || value != "readable" || f.secrets[1].Value != lost || f.secrets[2].Value != wrongBinding || f.secrets[3].Value != "enc:v1:not-really-an-envelope" || f.secrets[4].Value != "private-plaintext" {
		t.Fatalf("rotation changed unreadable rows or lost readable value: %v", err)
	}
}

func TestCredentialRotationHTTPReportsOnlyCommittedAttemptProperty(t *testing.T) {
	old, _, rotating := maintenanceCiphers(t)
	for discarded := 1; discarded <= 4; discarded++ {
		for repaired := 0; repaired <= discarded; repaired++ {
			b := authorityMember()
			f := &credentialMaintenance{team: "team"}
			for i := range discarded {
				row := store.Secret{Name: fmt.Sprintf("SECRET_%d", i), Pipeline: "ci", Masked: true, Value: "corrupt"}
				f.rolledBack = append(f.rolledBack, row)
				if i < repaired {
					var err error
					row.Value, err = old.SealBound(secrets.PurposeSecret, "team", row.Name, "ci", false, true, "private")
					if err != nil {
						t.Fatal(err)
					}
					f.secrets = append(f.secrets, row)
				}
			}
			f.secrets = append(f.secrets, store.Secret{Name: "STILL_BROKEN", Value: "corrupt"})
			w := authorityHTTP(maintenanceServer(t, b, f).WithSecretsCipher(rotating).Handler(), b, "POST", "/api/v1/secrets/rotate", "")
			var body struct {
				Rotated int
				Skipped []struct{ Name, Pipeline string }
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Rotated != repaired || len(body.Skipped) != 1 || body.Skipped[0].Name != "STILL_BROKEN" {
				t.Fatalf("discarded=%d repaired=%d: HTTP %d %s", discarded, repaired, w.Code, w.Body.String())
			}
		}
	}
}

func TestCredentialRotationHTTPGitFailureCommitsNothing(t *testing.T) {
	old, _, rotating := maintenanceCiphers(t)
	for _, failure := range []string{"envelope", "team"} {
		sealed, err := old.SealBound(secrets.PurposeSecret, "team", "GOOD", "", false, true, "readable")
		if err != nil {
			t.Fatal(err)
		}
		row := store.GitCredential{Team: "team", Host: "git.invalid", Secret: "private-broken-envelope"}
		if failure == "team" {
			row.Team = "foreign"
			row.Secret, err = old.SealBound(secrets.PurposeGitCredential, "foreign", "git-credential:git.invalid", "git-credential", false, true, "foreign-private")
			if err != nil {
				t.Fatal(err)
			}
		}
		b := authorityMember()
		f := &credentialMaintenance{team: "team", secrets: []store.Secret{{Name: "GOOD", Masked: true, Value: sealed}}, git: []store.GitCredential{row}}
		w := authorityHTTP(maintenanceServer(t, b, f).WithSecretsCipher(rotating).Handler(), b, "POST", "/api/v1/secrets/rotate", "")
		assertEngineError(t, w, 500, apierr.Internal)
		if f.secrets[0].Value != sealed || f.git[0] != row {
			t.Fatal("failed rotation changed a row")
		}
	}
}

func TestCredentialMaintenanceHTTPDependenciesAndTransactionRefusals(t *testing.T) {
	_, _, cipher := maintenanceCiphers(t)
	for _, route := range []struct{ method, path string }{{"POST", "/api/v1/secrets/rotate"}, {"GET", "/api/v1/team/git-credentials/releases"}} {
		for _, failure := range []string{"nil", "foreign", "resolver", "private", "revoked", "grant", "default"} {
			t.Run(route.path+"/"+failure, func(t *testing.T) {
				b := authorityMember()
				f := &credentialMaintenance{team: "team"}
				code, calls := apierr.Internal, 0
				switch failure {
				case "nil":
					f.missing, code = true, apierr.PolicyMissing
				case "foreign":
					f.team, code = "foreign", apierr.PolicyMissing
				case "resolver":
					f.resolveErr = errors.New("private resolver diagnostic")
				case "private":
					f.err, calls = errors.New("private store diagnostic"), 1
				case "revoked":
					f.err, calls, code = &apierr.Error{Code: apierr.Unauthenticated, Message: "revoked before transaction"}, 1, apierr.Unauthenticated
				case "grant":
					f.err, calls, code = &apierr.Error{Code: apierr.Forbidden, Message: "membership changed", Details: map[string]any{"reason": apierr.ReasonMissingGrant}}, 1, apierr.Forbidden
				case "default":
					code = apierr.PolicyMissing
				}
				s := maintenanceServer(t, b, f).WithSecretsCipher(cipher)
				if failure == "default" {
					s.WithCredentialRotator(nil).WithSourceAuditReader(nil)
				}
				w := authorityHTTP(s.Handler(), b, route.method, route.path, "")
				assertEngineError(t, w, apierr.Status(code), code)
				if f.calls != calls || strings.Contains(w.Body.String(), "private") {
					t.Fatalf("HTTP %d %s calls=%d", w.Code, w.Body.String(), f.calls)
				}
			})
		}
	}
}

func TestCredentialRotationHTTPBodyCipherAndCSRF(t *testing.T) {
	_, _, cipher := maintenanceCiphers(t)
	for _, invalid := range []string{"body", "null", "object", "no key", "no bound key", "csrf"} {
		b := authorityMember()
		f := &credentialMaintenance{team: "team"}
		s := maintenanceServer(t, b, f).WithSecretsCipher(cipher)
		body, code := "", apierr.BadRequest
		switch invalid {
		case "body":
			body = "private credential"
		case "null":
			body = "null"
		case "object":
			body = `{"team":"foreign"}`
		case "no key":
			s.WithSecretsCipher(nil)
			code = apierr.Unavailable
		case "no bound key":
			s.WithSecretsCipher(plainCipher{})
			code = apierr.Unavailable
		case "csrf":
			p := &b.snapshot.Authorities[0].Principal
			p.Class, p.Authority = capability.CredentialSession, sessionReference(*p)
			code = apierr.Forbidden
		}
		r := httptest.NewRequest("POST", "/api/v1/secrets/rotate", strings.NewReader(body))
		if invalid == "csrf" {
			r.AddCookie(&http.Cookie{Name: "console-session", Value: b.raw})
		} else {
			r.Header.Set("Authorization", "Bearer "+b.raw)
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		assertEngineError(t, w, apierr.Status(code), code)
		if f.calls != 0 {
			t.Fatal("invalid rotation reached transaction")
		}
	}
}

func TestCredentialAuditHTTPProjectsOnlyAuthorizedMetadata(t *testing.T) {
	property := func(private, selector string) bool {
		digest := sha256.Sum256([]byte(private))
		private = "private-" + hex.EncodeToString(digest[:])
		b := authorityMember()
		f := &credentialMaintenance{team: "team", audits: []capability.SourceReleaseAudit{{
			ID: private, Team: "team", CredentialID: private, Host: "git.invalid", RepositoryKey: "repo",
			RunID: "run", NodeID: "job", Generation: 7, MachineID: "runner", OwnerAccountID: private,
			CapabilityPrefix: "capability", ReleasedAt: time.Unix(200, 456).UTC(),
		}}}
		w := authorityHTTP(maintenanceServer(t, b, f).Handler(), b, "GET", "/api/v1/team/git-credentials/releases?"+url.Values{"team": {selector}, "run": {selector}}.Encode(), "")
		var body struct{ Releases []map[string]json.RawMessage }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Releases) != 1 || w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), private) {
			return false
		}
		want := map[string]json.RawMessage{"host": []byte(`"git.invalid"`), "repository": []byte(`"repo"`), "run_id": []byte(`"run"`), "job_id": []byte(`"job"`), "generation": []byte(`7`), "runner": []byte(`"runner"`), "capability_prefix": []byte(`"capability"`), "released_at": []byte(`200`)}
		return reflect.DeepEqual(body.Releases[0], want) && f.resolves == 1 && f.calls == 1
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 64}); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialAuditHTTPRefusesForeignRowsWithoutPartialList(t *testing.T) {
	b := authorityMember()
	f := &credentialMaintenance{team: "team", audits: []capability.SourceReleaseAudit{{Team: "team", Host: "partial.invalid"}, {Team: "foreign", Host: "foreign.invalid"}}}
	w := authorityHTTP(maintenanceServer(t, b, f).Handler(), b, "GET", "/api/v1/team/git-credentials/releases", "")
	assertEngineError(t, w, 500, apierr.Internal)
	if strings.Contains(w.Body.String(), "invalid") {
		t.Fatal("foreign audit returned a partial list")
	}
}

func TestCredentialMaintenanceHTTPMemberTransports(t *testing.T) {
	_, _, cipher := maintenanceCiphers(t)
	for _, class := range []capability.CredentialClass{capability.CredentialBearer, capability.CredentialSession, capability.CredentialPeer} {
		for _, route := range []struct{ method, path string }{{"POST", "/api/v1/secrets/rotate"}, {"GET", "/api/v1/team/git-credentials/releases"}} {
			b := authorityMember()
			p := &b.snapshot.Authorities[0].Principal
			p.Class = class
			if class == capability.CredentialSession {
				p.Authority = sessionReference(*p)
			} else if class == capability.CredentialPeer {
				p.Authority = capability.AuthorityRef{}
				p.Capability.Subject = capability.Subject{Kind: capability.SubjectLocal, ID: "uid:1000"}
			}
			f := &credentialMaintenance{team: "team"}
			w := authorityHTTP(maintenanceServer(t, b, f).WithSecretsCipher(cipher).Handler(), b, route.method, route.path, "")
			if w.Code != 200 || f.calls != 1 {
				t.Fatalf("class %s HTTP %d %s calls=%d", class, w.Code, w.Body.String(), f.calls)
			}
			if route.method == "GET" {
				var body struct{ Releases []json.RawMessage }
				if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Releases == nil || len(body.Releases) != 0 {
					t.Fatalf("empty audits=%s", w.Body.String())
				}
			}
		}
	}
}

type plainCipher struct{}

func (plainCipher) Seal(s string) (string, error) { return s, nil }
func (plainCipher) Open(s string) (string, error) { return s, nil }

type rotationCipherFailure struct {
	*secrets.Cipher
	sealErr error
	openErr error
}

func (c rotationCipherFailure) SealBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, plain string) (string, error) {
	if c.sealErr != nil {
		return "", c.sealErr
	}
	return c.Cipher.SealBound(purpose, team, name, scope, shared, masked, plain)
}

func (c rotationCipherFailure) OpenBound(purpose secrets.Purpose, team, name, scope string, shared, masked bool, value string) (string, error) {
	if c.openErr != nil {
		return "", c.openErr
	}
	return c.Cipher.OpenBound(purpose, team, name, scope, shared, masked, value)
}

func TestCredentialRotationHTTPCipherFailuresCommitNothing(t *testing.T) {
	old, _, rotating := maintenanceCiphers(t)
	for _, resource := range []string{"secret", "git"} {
		for _, failure := range []string{"key location", "key read", "provider", "provider API error"} {
			b := authorityMember()
			f := &credentialMaintenance{team: "team"}
			if resource == "secret" {
				value, err := old.SealBound(secrets.PurposeSecret, "team", "TOKEN", "", false, true, "private-value")
				if err != nil {
					t.Fatal(err)
				}
				f.secrets = []store.Secret{{Name: "TOKEN", Masked: true, Value: value}}
			} else {
				value, err := old.SealBound(secrets.PurposeGitCredential, "team", "git-credential:git.invalid", "git-credential", false, true, "private-value")
				if err != nil {
					t.Fatal(err)
				}
				f.git = []store.GitCredential{{Team: "team", Host: "git.invalid", Secret: value}}
			}
			beforeSecrets, beforeGit := append([]store.Secret(nil), f.secrets...), append([]store.GitCredential(nil), f.git...)
			cipher := rotationCipherFailure{Cipher: rotating}
			code := apierr.Internal
			switch failure {
			case "key location":
				cipher.sealErr, code = secrets.ErrKeyRefused, apierr.Conflict
			case "key read":
				cipher.openErr, code = secrets.ErrKeyRefused, apierr.Conflict
			case "provider":
				cipher.sealErr = errors.New("private provider diagnostic")
			case "provider API error":
				cipher.sealErr = &apierr.Error{Code: apierr.Forbidden, Message: "private provider diagnostic", Hint: "private hint", Details: map[string]any{"private": "diagnostic"}}
			}
			w := authorityHTTP(maintenanceServer(t, b, f).WithSecretsCipher(cipher).Handler(), b, "POST", "/api/v1/secrets/rotate", "")
			assertEngineError(t, w, apierr.Status(code), code)
			if !reflect.DeepEqual(f.secrets, beforeSecrets) || !reflect.DeepEqual(f.git, beforeGit) || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("%s/%s HTTP %d %s", resource, failure, w.Code, w.Body.String())
			}
		}
	}
}

func TestCredentialMaintenanceHTTPMissingAdaptersNeverUseLegacyStore(t *testing.T) {
	s, cipher := credentialMutationStore(t)
	for _, team := range []store.Team{"team", "foreign"} {
		tenant, err := s.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		if err := teststore.SeedSecret(s, tenant.Team(), store.Secret{Name: "UNCHANGED", Value: "original-envelope", Masked: true}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	b := authorityMember()
	h := mutationServerConfig(t, s, cipher, b).
		WithCredentialRotator(func(context.Context, store.Team) (controller.CredentialRotator, error) { return nil, nil }).
		WithSourceAuditReader(func(context.Context, store.Team) (controller.SourceAuditReader, error) { return nil, nil }).Handler()
	for _, route := range []struct{ method, path string }{{"POST", "/api/v1/secrets/rotate"}, {"GET", "/api/v1/team/git-credentials/releases"}} {
		w := authorityHTTP(h, b, route.method, route.path, "")
		assertEngineError(t, w, 500, apierr.PolicyMissing)
	}
	for _, team := range []store.Team{"team", "foreign"} {
		tenant, err := s.ForTeam(t.Context(), team)
		if err != nil {
			t.Fatal(err)
		}
		row, err := tenant.GetSecretRow("UNCHANGED", "")
		if err != nil || row.Value != "original-envelope" {
			t.Fatalf("legacy fallback changed %s: %+v %v", team, row, err)
		}
	}
}

func TestCredentialMaintenanceHTTPUsesStoreTransactions(t *testing.T) {
	f := newSessionFlow(t)
	if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role='owner' WHERE team='team' AND account_id='member'`); err != nil {
		t.Fatal(err)
	}
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{34}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	tenant, err := f.store.ForTeam(t.Context(), "team")
	if err != nil {
		t.Fatal(err)
	}
	row := store.Secret{Name: "TOKEN", Pipeline: "deploy", Masked: true, Value: "private value"}
	row.Value, err = controller.SealSecretValue(cipher, "team", &row, row.Value)
	if err != nil {
		t.Fatal(err)
	}
	if err := teststore.SeedSecret(f.store, tenant.Team(), row, time.Now()); err != nil {
		t.Fatal(err)
	}
	gitValue, err := store.SealGitCredential(cipher, "team", store.GitCredential{Host: "git.example.test", Kind: store.GitCredentialHTTPS, Secret: "private key"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tenant.WriteGitCredential(t.Context(), credentialSeedAuthority(t, f.store, "team"), gitValue); err != nil {
		t.Fatal(err)
	}
	handler := f.server.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("POST", "/api/v1/secrets/rotate", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("real rotation: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Rotated int `json:"rotated"`
		Git     int `json:"git_credentials"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Rotated != 1 || result.Git != 1 {
		t.Fatalf("rotation response: %s,%v", w.Body.String(), err)
	}
	got, err := tenant.GetSecretRow("TOKEN", "deploy")
	if err != nil || got.Value == row.Value {
		t.Fatalf("secret did not rotate: %+v,%v", got, err)
	}
	plain, err := controller.OpenSecretValue(cipher, "team", got)
	if err != nil || plain != "private value" {
		t.Fatalf("rotated secret unreadable: %v", err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("GET", "/api/v1/team/git-credentials/releases", ""))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"releases":[]`) {
		t.Fatalf("real audit reader: %d %s", w.Code, w.Body.String())
	}
	if _, err := teststore.SQL(t, f.store).ExecContext(t.Context(), `UPDATE memberships SET role='reader' WHERE team='team' AND account_id='member'`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, f.request("POST", "/api/v1/secrets/rotate", ""))
	if w.Code != http.StatusForbidden {
		t.Fatalf("demoted rotation: %d %s", w.Code, w.Body.String())
	}
	after, err := tenant.GetSecretRow("TOKEN", "deploy")
	if err != nil || after.Value != got.Value {
		t.Fatalf("demoted request changed secret: %+v,%v", after, err)
	}
}
