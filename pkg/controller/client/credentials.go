package client

import (
	"context"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type tokenManager struct {
	client     *engineClient
	base       string
	deployment bool
}

var _ controllerwire.TokenManager = (*tokenManager)(nil)

// NewTokenManager selects one credential namespace for every operation. The
// controller still authorizes each route using its independent credential rules.
func NewTokenManager(options EngineOptions, deployment bool) (controllerwire.TokenManager, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	base := "/api/v1/tokens"
	if deployment {
		base = "/api/v1/operator/tokens"
	}
	return &tokenManager{client: c, base: base, deployment: deployment}, nil
}

func (c *tokenManager) validKind(kind string) bool {
	if kind == "" {
		return true
	}
	if c.deployment {
		return kind == "service" || kind == "deployment"
	}
	return kind == "member" || kind == "runner"
}

func (c *tokenManager) ListTokens(ctx context.Context, q controllerwire.TokenQuery) (controllerwire.Tokens, error) {
	var result controllerwire.Tokens
	if !c.validKind(q.Kind) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "token type does not belong to this credential namespace"}
	}
	params := url.Values{}
	if q.Kind != "" {
		params.Set("kind", q.Kind)
	}
	if q.IncludeRevoked {
		params.Set("include_revoked", "true")
	}
	path := c.base
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	err := c.client.request(ctx, "GET", c.base, path, "", nil, &result)
	if err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, token := range result.Tokens {
		if !c.validMetadata(token) || seen[token.Prefix] {
			return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned invalid or repeated token metadata"}
		}
		seen[token.Prefix] = true
	}
	return result, nil
}

func (c *tokenManager) ReadToken(ctx context.Context, prefix string) (controllerwire.TokenMetadata, error) {
	var result controllerwire.TokenMetadata
	if !validSelector(prefix) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid token prefix"}
	}
	err := c.client.request(ctx, "GET", c.base+"/{token}", c.base+"/"+url.PathEscape(prefix), "", nil, &result)
	if err == nil && (!c.validMetadata(result) || result.Prefix != prefix) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned conflicting token metadata"}
	}
	return result, err
}

func (c *tokenManager) CreateToken(ctx context.Context, input controllerwire.TokenCreate) (controllerwire.TokenCreated, error) {
	var result controllerwire.TokenCreated
	if !c.validKind(input.Kind) || input.TTLSecs != nil && *input.TTLSecs < 0 || !validSelector(input.IdempotencyKey) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "token creation requires a namespace-compatible type, nonnegative TTL and idempotency key"}
	}
	err := c.client.request(ctx, "POST", c.base, c.base, input.IdempotencyKey, input, &result)
	if err == nil && (result.Token == "" || !c.validMetadata(result.Metadata) || input.Kind != "" && result.Metadata.Kind != input.Kind) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an incomplete token creation"}
	}
	return result, err
}

func (c *tokenManager) RotateToken(ctx context.Context, prefix string, input controllerwire.TokenRotate) (controllerwire.TokenRotated, error) {
	var result controllerwire.TokenRotated
	if !validSelector(prefix) || input.GraceSecs < 0 || input.TTLSecs != nil && *input.TTLSecs < 0 || !validSelector(input.IdempotencyKey) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "token rotation requires a prefix, nonnegative grace and TTL, and idempotency key"}
	}
	err := c.client.request(ctx, "POST", c.base+"/{token}/rotate", c.base+"/"+url.PathEscape(prefix)+"/rotate", input.IdempotencyKey, input, &result)
	if err == nil && (result.Token == "" || !c.validMetadata(result.Metadata) || result.OldPrefix != prefix || result.OldRevokedAt <= 0) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an incomplete token rotation"}
	}
	return result, err
}

func (c *tokenManager) RevokeToken(ctx context.Context, prefix string) (controllerwire.TokenRevoked, error) {
	var result controllerwire.TokenRevoked
	if !validSelector(prefix) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid token prefix"}
	}
	err := c.client.request(ctx, "DELETE", c.base+"/{token}", c.base+"/"+url.PathEscape(prefix), "", nil, &result)
	if err == nil && (result.Prefix != prefix || !result.Revoked) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller did not confirm token revocation"}
	}
	return result, err
}

func (c *tokenManager) validMetadata(token controllerwire.TokenMetadata) bool {
	return validSelector(token.Prefix) && token.Kind != "" && c.validKind(token.Kind) && !strings.ContainsAny(token.Principal, "\r\n\x00")
}
