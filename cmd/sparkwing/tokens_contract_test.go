package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type tokenManagerFake struct {
	metadata controllerwire.TokenMetadata
	create   controllerwire.TokenCreate
	rotate   controllerwire.TokenRotate
	prefix   string
	err      error
}

func (f *tokenManagerFake) ListTokens(context.Context, controllerwire.TokenQuery) (controllerwire.Tokens, error) {
	return controllerwire.Tokens{Tokens: []controllerwire.TokenMetadata{f.metadata}}, f.err
}
func (f *tokenManagerFake) ReadToken(_ context.Context, prefix string) (controllerwire.TokenMetadata, error) {
	f.prefix = prefix
	return f.metadata, f.err
}
func (f *tokenManagerFake) CreateToken(_ context.Context, input controllerwire.TokenCreate) (controllerwire.TokenCreated, error) {
	f.create = input
	return controllerwire.TokenCreated{Token: "swk_secret", Metadata: f.metadata}, f.err
}
func (f *tokenManagerFake) RotateToken(_ context.Context, prefix string, input controllerwire.TokenRotate) (controllerwire.TokenRotated, error) {
	f.prefix, f.rotate = prefix, input
	return controllerwire.TokenRotated{Token: "swk_rotated", Metadata: f.metadata, OldPrefix: prefix, OldRevokedAt: 42}, f.err
}
func (f *tokenManagerFake) RevokeToken(_ context.Context, prefix string) (controllerwire.TokenRevoked, error) {
	f.prefix = prefix
	return controllerwire.TokenRevoked{Prefix: prefix, Revoked: true}, f.err
}

func TestTokenCommandsUseFinalGrammarAndStructuredResults(t *testing.T) {
	f := &tokenManagerFake{metadata: controllerwire.TokenMetadata{Prefix: "swk_prefix", Kind: "member", Principal: "build", Scopes: []capability.Grant{capability.RunsRead, capability.LogsRead}}}
	inv, err := clisurface.Parse("sparkwing", []string{"tokens", "create", "--type", "member", "--principal", "build", "--scope", "runs.read,logs.read", "--ttl", "0", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := manageTokens(t.Context(), inv, f, &out); err != nil {
		t.Fatal(err)
	}
	if f.create.Kind != "member" || f.create.Principal != "build" || f.create.TTLSecs == nil || *f.create.TTLSecs != 0 || f.create.IdempotencyKey == "" || !reflect.DeepEqual(f.create.Scopes, f.metadata.Scopes) {
		t.Fatalf("creation: %+v", f.create)
	}
	var created controllerwire.TokenCreated
	if err := json.Unmarshal(out.Bytes(), &created); err != nil || created.Token != "swk_secret" || !reflect.DeepEqual(created.Metadata, f.metadata) {
		t.Fatalf("created: %+v %v", created, err)
	}
	out.Reset()
	inv, _ = clisurface.Parse("sparkwing", []string{"tokens", "rotate", "--token", "swk_prefix", "--grace", "0", "--ttl", "1h", "-o", "json"})
	if err := manageTokens(t.Context(), inv, f, &out); err != nil || f.prefix != "swk_prefix" || f.rotate.GraceSecs != 0 || f.rotate.TTLSecs == nil || *f.rotate.TTLSecs != 3600 || f.rotate.IdempotencyKey == "" {
		t.Fatalf("rotate: %+v %s %v", f.rotate, out.String(), err)
	}
	var rotated controllerwire.TokenRotated
	if err := json.Unmarshal(out.Bytes(), &rotated); err != nil || rotated.OldRevokedAt != 42 {
		t.Fatalf("rotation result: %s %v", out.String(), err)
	}
}

func TestTokenOutputKeepsSecretsOutOfMetadataReads(t *testing.T) {
	f := &tokenManagerFake{metadata: controllerwire.TokenMetadata{Prefix: "swk_prefix", Kind: "runner", Principal: "desktop", Scopes: []capability.Grant{capability.NodesClaim}}}
	for _, command := range [][]string{{"list"}, {"list", "--token", "swk_prefix"}, {"revoke", "--token", "swk_prefix"}} {
		inv, err := clisurface.Parse("sparkwing", append(append([]string{"tokens"}, command...), "-o", "plain"))
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := manageTokens(t.Context(), inv, f, &out); err != nil || out.String() != "swk_prefix\n" {
			t.Fatalf("plain %v: %q %v", command, out.String(), err)
		}
	}
	inv, _ := clisurface.Parse("sparkwing", []string{"tokens", "create", "-o", "plain"})
	var out bytes.Buffer
	if err := manageTokens(t.Context(), inv, f, &out); err != nil || out.String() != "swk_secret\n" || f.create.TTLSecs != nil {
		t.Fatalf("plain creation: %q %v", out.String(), err)
	}
}

func TestTokenValidationPrecedesCredentialResolution(t *testing.T) {
	for _, args := range [][]string{{"tokens", "create", "--ttl", "-1h"}, {"tokens", "rotate", "--token", "prefix", "--grace", "1ms"}, {"tokens", "rotate", "--token", "prefix", "--ttl", "-1"}, {"operator", "tokens", "create", "--type", "member"}, {"tokens", "create", "--type", "service"}, {"tokens", "list", "--type", "user"}} {
		inv, err := clisurface.Parse("sparkwing", args)
		if err == nil {
			_, _, err = parseTokenMutation(inv)
		}
		if err == nil {
			t.Fatalf("invalid invocation accepted: %v", args)
		}
	}
	failure := &apierr.Error{Code: apierr.Forbidden, Message: "token owner required", Details: map[string]any{"reason": "not_owner"}}
	f := &tokenManagerFake{err: failure}
	inv, _ := clisurface.Parse("sparkwing", []string{"tokens", "revoke", "--token", "prefix", "-o", "json"})
	var out bytes.Buffer
	if err := manageTokens(t.Context(), inv, f, &out); !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("authorization refusal: %s %v", out.String(), err)
	}
}

func TestTokenMetadataDisplaysExactGrants(t *testing.T) {
	var out bytes.Buffer
	tokens := []controllerwire.TokenMetadata{{Prefix: "prefix", Kind: "member", Principal: "build", Scopes: []capability.Grant{capability.TeamAdmin, capability.RunsRead}}}
	if err := writeTokenMetadata(&out, tokens, "pretty"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "team.admin,runs.read") || strings.Contains(out.String(), "*") {
		t.Fatalf("grants changed: %s", out.String())
	}
}
