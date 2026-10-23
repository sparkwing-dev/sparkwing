package controller_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/pkg/apierr"
	"golang.org/x/crypto/ssh"
)

type credentialPutCapture struct {
	controller.CredentialWriter
	row   store.GitCredential
	actor capability.Authorized
	calls int
}

func (*credentialPutCapture) Team() store.Team { return "team" }

func (c *credentialPutCapture) WriteGitCredential(_ context.Context, a capability.Authorized, sealed store.SealedGitCredential) (store.GitCredential, error) {
	row := sealed.Row()
	c.row, c.actor = row, a
	c.calls++
	return row, nil
}

func TestCredentialSSHWritePinsHostAndKeepsKeyPrivate(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	key := deployKey(t)
	cipher, err := secrets.NewCipher(bytes.Repeat([]byte{35}, secrets.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"default port", "explicit port", "zero port", "invalid key", "scan failure"} {
		t.Run(name, func(t *testing.T) {
			var order []string
			principal := capability.Principal{
				Class:      capability.CredentialBearer,
				Authority:  capability.AuthorityRef{Kind: capability.AuthorityCapability, ID: "member"},
				Capability: capability.Capability{Audience: capability.Controller, Team: "team", Subject: capability.Subject{Kind: capability.SubjectAccount, ID: "account"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.TeamAdmin}},
			}
			writer := &credentialPutCapture{}
			server := controller.BindTenantPortForTest[controller.CredentialWriter](controller.New(nil, nil).WithSecretsCipher(cipher).WithAuthorization(routepolicy.Authorization{
				Authenticator: policyAuth{principal: principal, order: &order}, Bindings: policyBindings{order: &order},
			}), func(context.Context, store.Team) (controller.CredentialWriter, error) { return writer, nil })
			port, scans := 22, 0
			body := map[string]any{"host_wide": true, "host": "Git.Invalid", "kind": "ssh", "private_key": key}
			status, code := 200, apierr.Code("")
			switch name {
			case "explicit port":
				port, body["port"] = 2222, 2222
			case "zero port":
				body["port"], status, code = 0, 400, apierr.BadRequest
			case "invalid key":
				body["private_key"], status, code = "private credential diagnostic", 400, apierr.BadRequest
			case "scan failure":
				status, code = 503, apierr.Unavailable
			}
			controller.SetHostKeyScan(server, func(_ context.Context, host string, gotPort int) (ssh.PublicKey, error) {
				scans++
				if host != "git.invalid" || gotPort != port {
					t.Fatalf("scan %s:%d", host, gotPort)
				}
				if name == "scan failure" {
					return nil, errors.New("private credential scan diagnostic")
				}
				return hostKey, nil
			})
			data, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/v1/team/git-credentials?team=foreign", bytes.NewReader(data))
			r.Header.Set("Authorization", "Bearer member")
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, r)
			if w.Code != status || strings.Contains(w.Body.String(), key) || strings.Contains(w.Body.String(), "private credential") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			if code != "" {
				var result apierr.Envelope
				if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Error == nil || result.Error.Code != code || writer.calls != 0 || name != "scan failure" && scans != 0 {
					t.Fatalf("HTTP %d %s writes=%d scans=%d", w.Code, w.Body.String(), writer.calls, scans)
				}
				return
			}
			var result controller.GitCredentialJSON
			if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Confirmed || result.PublicKey == "" || result.HostKey == "" || result.Fingerprint != ssh.FingerprintSHA256(hostKey) || writer.calls != 1 || scans != 1 || writer.actor.Principal.Authority != principal.Authority || writer.row.CreatedBy != "account" || writer.row.Team != "team" {
				t.Fatalf("result=%+v row=%+v scans=%d", result, writer.row, scans)
			}
			plain, err := cipher.OpenBound(secrets.PurposeGitCredential, "team", "git-credential:git.invalid", "git-credential:host:git.invalid", false, true, writer.row.Secret)
			if err != nil || plain != key {
				t.Fatalf("stored key did not open: %v", err)
			}
		})
	}
}
