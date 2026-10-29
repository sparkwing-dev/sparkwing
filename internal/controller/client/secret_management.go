package client

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/secretname"
)

var _ controllerwire.SecretManager = (*engineClient)(nil)

// NewSecretManager uses the common authenticated transport and route-owned
// retry policy. Raw secret values are never part of metadata responses.
func NewSecretManager(options EngineOptions) (controllerwire.SecretManager, error) {
	return newEngineClient(options)
}

func (c *engineClient) ListSecrets(ctx context.Context) (controllerwire.Secrets, error) {
	var result controllerwire.Secrets
	if err := c.request(ctx, "GET", "/api/v1/secrets", "/api/v1/secrets", "", nil, &result); err != nil {
		return result, err
	}
	seen := map[controllerwire.SecretSelector]bool{}
	for _, secret := range result.Secrets {
		key := controllerwire.SecretSelector{Name: secret.Name, Repo: secret.RepoKey, Pipeline: secret.Pipeline}
		if !validSecretMetadata(secret) || seen[key] {
			return result, secretProtocolError("controller returned invalid or repeated secret metadata")
		}
		seen[key] = true
	}
	return result, nil
}

func (c *engineClient) ReadSecret(ctx context.Context, selector controllerwire.SecretSelector) (controllerwire.SecretValue, error) {
	var result controllerwire.SecretValue
	if !validSecretSelector(selector) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid secret selector"}
	}
	err := c.request(ctx, "GET", "/api/v1/secrets/{name}", secretPath(selector), "", nil, &result)
	if err == nil && (!validSecretMetadata(result.SecretMetadata) || result.Name != selector.Name || result.Pipeline != selector.Pipeline && !(selector.Pipeline != "" && result.Pipeline == "" && result.Shared)) {
		err = secretProtocolError("controller returned conflicting secret identity")
	}
	return result, err
}

func (c *engineClient) WriteSecret(ctx context.Context, input controllerwire.SecretWrite) (controllerwire.SecretWritten, error) {
	var result controllerwire.SecretWritten
	if !validSecretSelector(controllerwire.SecretSelector{Name: input.Name, Repo: input.Repo, Pipeline: input.Pipeline}) || input.Pipeline != "" && input.Shared || input.OnlyOn != "" && !validSelector(input.OnlyOn) || !validSelector(input.IdempotencyKey) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "secret write requires a valid name, scope, pool and idempotency key"}
	}
	err := c.request(ctx, "POST", "/api/v1/secrets", "/api/v1/secrets", input.IdempotencyKey, input, &result)
	got := result.Secret
	if err == nil && (!validSecretMetadata(got) || got.Name != input.Name || got.Pipeline != input.Pipeline || got.Masked != input.Masked || got.Shared != input.Shared || input.OnlyOn != "" && got.OnlyOn != input.OnlyOn || !got.Bound) {
		err = secretProtocolError("controller did not confirm encrypted secret scope and placement")
	}
	return result, err
}

func (c *engineClient) DeleteSecret(ctx context.Context, selector controllerwire.SecretSelector) (controllerwire.SecretDeletion, error) {
	result := controllerwire.SecretDeletion{Name: selector.Name, Repo: selector.Repo, Pipeline: selector.Pipeline}
	if !validSecretSelector(selector) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "invalid secret selector"}
	}
	err := c.request(ctx, "DELETE", "/api/v1/secrets/{name}", secretPath(selector), "", nil, nil)
	var failure *apierr.Error
	if errors.As(err, &failure) && failure.Code == apierr.NotFound {
		return result, nil
	}
	result.Deleted = err == nil
	return result, err
}

func (c *engineClient) RotateSecrets(ctx context.Context, input controllerwire.SecretRotationRequest) (controllerwire.SecretRotation, error) {
	var result controllerwire.SecretRotation
	if !validSelector(input.IdempotencyKey) {
		return result, &apierr.Error{Code: apierr.BadRequest, Message: "secret rotation requires an idempotency key"}
	}
	err := c.request(ctx, "POST", "/api/v1/secrets/rotate", "/api/v1/secrets/rotate", input.IdempotencyKey, nil, &result)
	if err != nil {
		return result, err
	}
	if result.Rotated < 0 {
		return result, secretProtocolError("controller returned a negative secret rotation count")
	}
	seen := map[controllerwire.SecretSelector]bool{}
	for _, skipped := range result.Skipped {
		if !validSecretSelector(skipped) || seen[skipped] {
			return result, secretProtocolError("controller returned invalid or repeated skipped secret rows")
		}
		seen[skipped] = true
	}
	return result, nil
}

func secretPath(selector controllerwire.SecretSelector) string {
	path := "/api/v1/secrets/" + url.PathEscape(selector.Name)
	if selector.Pipeline != "" {
		path += "?" + url.Values{"repo": {selector.Repo}, "pipeline": {selector.Pipeline}}.Encode()
	}
	return path
}

// validSecretSelector holds a pipeline row's repository and pipeline together,
// since a pipeline secret belongs to exactly one repository.
func validSecretSelector(selector controllerwire.SecretSelector) bool {
	return secretname.Validate(selector.Name) == nil && (selector.Repo == "") == (selector.Pipeline == "") && (selector.Pipeline == "" || validSelector(selector.Pipeline) && validSelector(selector.Repo))
}

func validSecretMetadata(secret controllerwire.SecretMetadata) bool {
	return validSecretSelector(controllerwire.SecretSelector{Name: secret.Name, Repo: secret.RepoKey, Pipeline: secret.Pipeline}) && !(secret.Pipeline != "" && secret.Shared) && !strings.ContainsAny(secret.Principal, "\r\n\x00") && (secret.OnlyOn == "" || validSelector(secret.OnlyOn))
}

func secretProtocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}
