package client

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type tokenManager struct {
	client     *engineClient
	base       string
	deployment bool
}

var _ controllerwire.TokenManager = (*tokenManager)(nil)

// RunnerTokenManager enrolls machines and manages their team credentials through one transport.
type RunnerTokenManager interface {
	controllerwire.TokenManager
	EnrollRunner(context.Context, controllerwire.EnrollRunnerRequest) (*controllerwire.CreatedToken, error)
}

// NewRunnerTokenManager binds enrollment and token management to the team namespace.
func NewRunnerTokenManager(options EngineOptions) (RunnerTokenManager, error) {
	c, err := newEngineClient(options)
	if err != nil {
		return nil, err
	}
	return &tokenManager{client: c, base: "/api/v1/tokens"}, nil
}

func (c *tokenManager) EnrollRunner(ctx context.Context, request controllerwire.EnrollRunnerRequest) (*controllerwire.CreatedToken, error) {
	if c.deployment {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "runner enrollment requires the team credential namespace"}
	}
	var result controllerwire.CreatedToken
	if err := c.client.request(ctx, "POST", "/api/v1/runners", "/api/v1/runners", "", request, &result); err != nil {
		return nil, err
	}
	if result.Capability == "" || !validSelector(result.Prefix) || result.Team == "" || len(result.Grants) == 0 {
		return nil, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned incomplete runner enrollment"}
	}
	return &result, nil
}

// NewTokenManager selects the credential namespace while the server authorizes every operation.
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

func tokenSubjectKind(kind string) capability.SubjectKind {
	switch kind {
	case "member":
		return capability.SubjectAccount
	case "runner":
		return capability.SubjectMachine
	case "service":
		return capability.SubjectService
	case "deployment":
		return capability.SubjectOperator
	default:
		return ""
	}
}

func (c *tokenManager) ListTokens(ctx context.Context, q controllerwire.TokenQuery) (controllerwire.Tokens, error) {
	result := controllerwire.Tokens{Tokens: []controllerwire.TokenMetadata{}}
	if !c.validKind(q.Kind) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "token type does not belong to this credential namespace"}
	}
	params := url.Values{}
	if q.Kind != "" {
		params.Set("kind", string(tokenSubjectKind(q.Kind)))
	}
	if q.IncludeRevoked {
		params.Set("include_revoked", "1")
	}
	path := c.base
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var response struct {
		Tokens []capability.Metadata `json:"tokens"`
	}
	if err := c.client.requestList(ctx, c.base, path, &response, "tokens"); err != nil {
		return result, err
	}
	seen := map[string]bool{}
	for _, row := range response.Tokens {
		token := tokenMetadata(row)
		if !c.validMetadata(token) || row.Audience != capability.Controller || row.IssuedAt.IsZero() || seen[token.Prefix] || q.Kind != "" && token.Kind != q.Kind {
			return controllerwire.Tokens{}, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned invalid or repeated token metadata"}
		}
		seen[token.Prefix] = true
		result.Tokens = append(result.Tokens, token)
	}
	return result, nil
}

func (c *tokenManager) ReadToken(ctx context.Context, prefix string) (controllerwire.TokenMetadata, error) {
	if !validSelector(prefix) {
		return controllerwire.TokenMetadata{}, &apierr.Error{Code: apierr.BadRequest, Message: "invalid token prefix"}
	}
	var row capability.Metadata
	err := c.client.request(ctx, "GET", c.base+"/{token}", c.base+"/"+url.PathEscape(prefix), "", nil, &row)
	result := tokenMetadata(row)
	if err == nil && (!c.validMetadata(result) || result.Prefix != prefix || row.Audience != capability.Controller || row.IssuedAt.IsZero()) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned conflicting token metadata"}
	}
	return result, err
}

func (c *tokenManager) CreateToken(ctx context.Context, input controllerwire.TokenCreate) (controllerwire.TokenCreated, error) {
	var result controllerwire.TokenCreated
	kind := input.Kind
	if kind == "" {
		kind = "member"
		if c.deployment {
			kind = "service"
		}
	}
	if !c.validKind(kind) || kind == "runner" || kind == "deployment" || input.TTLSecs != nil && *input.TTLSecs < 0 {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "token creation requires a member or service type and nonnegative TTL; runners require enrollment"}
	}
	expires, err := tokenExpiry(input.TTLSecs)
	if err != nil {
		return result, err
	}
	grants := make([]string, 0, len(input.Scopes))
	for _, grant := range input.Scopes {
		grants = append(grants, string(grant))
	}
	var request any = controllerwire.CreateTokenRequest{Grants: grants, ExpiresAt: expires, AllowTeamAdmin: input.AllowTeamAdmin}
	if c.deployment {
		request = controllerwire.CreateServiceTokenRequest{Name: input.Principal, Grants: grants, ExpiresAt: expires}
	}
	var response controllerwire.CreatedToken
	if err := c.client.request(ctx, "POST", c.base, c.base, "", request, &response); err != nil {
		return result, err
	}
	if response.Capability == "" || !validSelector(response.Prefix) {
		return result, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned incomplete credential issuance"}
	}
	result.Token = response.Capability
	result.Metadata = controllerwire.TokenMetadata{Prefix: response.Prefix, Kind: kind, Principal: response.Principal, Team: response.Team, Scopes: []capability.Grant{}, ExpiresAt: tokenTime(response.ExpiresAt)}
	for _, grant := range response.Grants {
		result.Metadata.Scopes = append(result.Metadata.Scopes, capability.Grant(grant))
	}
	return result, nil
}

func (c *tokenManager) RotateToken(ctx context.Context, prefix string, input controllerwire.TokenRotate) (controllerwire.TokenRotated, error) {
	var result controllerwire.TokenRotated
	if !validSelector(prefix) || input.GraceSecs < 0 || input.TTLSecs != nil && *input.TTLSecs < 0 {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "token rotation requires a prefix and nonnegative grace and TTL"}
	}
	expires, err := tokenExpiry(input.TTLSecs)
	if err != nil {
		return result, err
	}
	request := struct {
		GraceSecs int64      `json:"grace_secs"`
		ExpiresAt *time.Time `json:"expires_at,omitempty"`
	}{input.GraceSecs, expires}
	var response controllerwire.RotatedToken
	if err := c.client.request(ctx, "POST", c.base+"/{token}/rotate", c.base+"/"+url.PathEscape(prefix)+"/rotate", "", request, &response); err != nil {
		return result, err
	}
	result = controllerwire.TokenRotated{Token: response.Capability, Metadata: tokenMetadata(response.New), OldPrefix: response.OldPrefix, OldRevokedAt: response.OldRevokedAt.Unix()}
	if result.Token == "" || !c.validMetadata(result.Metadata) || response.New.Audience != capability.Controller || response.New.IssuedAt.IsZero() || result.OldPrefix != prefix || response.OldRevokedAt.IsZero() {
		return controllerwire.TokenRotated{}, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned incomplete credential rotation"}
	}
	return result, nil
}

func (c *tokenManager) RevokeToken(ctx context.Context, prefix string) (controllerwire.TokenRevoked, error) {
	if !validSelector(prefix) {
		return controllerwire.TokenRevoked{}, &apierr.Error{Code: apierr.BadRequest, Message: "invalid token prefix"}
	}
	err := c.client.request(ctx, "DELETE", c.base+"/{token}", c.base+"/"+url.PathEscape(prefix), "", nil, nil)
	return controllerwire.TokenRevoked{Prefix: prefix, Revoked: err == nil}, err
}

func (c *tokenManager) validMetadata(token controllerwire.TokenMetadata) bool {
	return validSelector(token.Prefix) && token.Kind != "" && c.validKind(token.Kind) && token.Principal != "" && !strings.ContainsAny(token.Principal, "\r\n\x00")
}

func tokenExpiry(seconds *int64) (*time.Time, error) {
	if seconds == nil || *seconds == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	if *seconds < 0 || *seconds > 253402300799-now.Unix() {
		return nil, &apierr.Error{Code: apierr.BadRequest, Message: "token TTL cannot be represented as an expiry timestamp"}
	}
	expires := time.Unix(now.Unix()+*seconds, int64(now.Nanosecond())).UTC()
	return &expires, nil
}

func tokenTime(value *time.Time) *int64 {
	if value == nil {
		return nil
	}
	seconds := value.Unix()
	return &seconds
}

func tokenMetadata(row capability.Metadata) controllerwire.TokenMetadata {
	kind := ""
	for _, candidate := range []string{"member", "runner", "service", "deployment"} {
		if tokenSubjectKind(candidate) == row.Subject.Kind {
			kind = candidate
		}
	}
	return controllerwire.TokenMetadata{
		Prefix: row.Prefix, Kind: kind, Principal: row.Subject.ID, Subject: &row.Subject,
		Team: row.Team, Scopes: append([]capability.Grant{}, row.Grants...), Metered: row.Metered,
		IssuedAt: tokenTime(&row.IssuedAt), ExpiresAt: tokenTime(row.ExpiresAt), LastUsedAt: tokenTime(row.LastUsedAt), RevokedAt: tokenTime(row.RevokedAt),
	}
}
