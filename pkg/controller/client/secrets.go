// Secrets CRUD against the controller.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Secret mirrors the wire shape of one secret row. Value is populated
// only on GetSecret; ListSecrets blanks it. Masked indicates whether
// the value should be redacted in run logs. Bound reports that the
// stored value is encrypted and tied to this row; false means the row
// is plaintext, or an envelope written before binding that a read
// will rebind.
type Secret struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Repo      string `json:"repo,omitempty"`
	Masked    bool   `json:"masked"`
	Shared    bool   `json:"shared,omitempty"`
	Bound     bool   `json:"bound"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CreateSecret uploads value under name, replacing any existing row.
// masked=false registers non-secret config (region, log level, etc).
func (c *Client) CreateSecret(ctx context.Context, name, value string, masked bool) error {
	return c.CreateSecretForRepo(ctx, name, value, "", masked, false)
}

// CreateSecretForRepo uploads value under name, owned by the given
// repository slug. An empty repo stores the secret unscoped, where only
// an admin reads it until shared is true, which opens it to every run.
func (c *Client) CreateSecretForRepo(ctx context.Context, name, value, repo string, masked, shared bool) error {
	body := map[string]any{"name": name, "value": value, "repo": repo, "masked": masked, "shared": shared}
	return c.post(ctx, "/api/v1/secrets", body, http.StatusNoContent, nil)
}

// GetSecret fetches one row including its value. Returns
// store.ErrNotFound when the secret doesn't exist.
func (c *Client) GetSecret(ctx context.Context, name string) (*Secret, error) {
	return c.getSecret(ctx, name, "")
}

// GetSecretForRun fetches the row owned by the repository of runID. The
// controller checks the caller's claim on that run, so this is how a
// runner working several runs names which one the read is for.
func (c *Client) GetSecretForRun(ctx context.Context, name, runID string) (*Secret, error) {
	return c.getSecret(ctx, name, queryParam("run", runID))
}

// GetSecretForRepo fetches the row a repository owns, falling back to
// the unscoped row. The repo hint is honored only for an admin
// caller; a narrower principal reads the repository of the run it
// holds a claim in.
func (c *Client) GetSecretForRepo(ctx context.Context, name, repo string) (*Secret, error) {
	return c.getSecret(ctx, name, queryParam("repo", repo))
}

func (c *Client) getSecret(ctx context.Context, name, query string) (*Secret, error) {
	u := fmt.Sprintf("%s/api/v1/secrets/%s%s", c.baseURL, url.PathEscape(name), query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var sec Secret
		if err := json.NewDecoder(resp.Body).Decode(&sec); err != nil {
			return nil, err
		}
		return &sec, nil
	case http.StatusNotFound:
		return nil, notFound(resp)
	default:
		return nil, readHTTPError(resp)
	}
}

// ListSecrets fetches every secret row with Value blanked by the
// server.
func (c *Client) ListSecrets(ctx context.Context) ([]Secret, error) {
	u := c.baseURL + "/api/v1/secrets"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readHTTPError(resp)
	}
	var body struct {
		Secrets []Secret `json:"secrets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Secrets, nil
}

// DeleteSecret removes the row by name. Returns store.ErrNotFound
// when no row existed.
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	return c.DeleteSecretForRepo(ctx, name, "")
}

// DeleteSecretForRepo removes the row a repository owns. An empty
// repo removes the unscoped row.
func (c *Client) DeleteSecretForRepo(ctx context.Context, name, repo string) error {
	u := fmt.Sprintf("%s/api/v1/secrets/%s%s", c.baseURL, url.PathEscape(name), queryParam("repo", repo))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return notFound(resp)
	default:
		return readHTTPError(resp)
	}
}

func queryParam(key, value string) string {
	if value == "" {
		return ""
	}
	return "?" + key + "=" + url.QueryEscape(value)
}
