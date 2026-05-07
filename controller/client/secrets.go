// Secrets CRUD against the controller.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/v2/orchestrator/store"
)

// Secret mirrors the wire shape of one secret row. Value is populated
// only on GetSecret; ListSecrets blanks it. Masked indicates whether
// the value should be redacted in run logs.
type Secret struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Masked    bool   `json:"masked"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// CreateSecret uploads value under name, replacing any existing row.
// masked=false registers non-secret config (region, log level, etc).
func (c *Client) CreateSecret(ctx context.Context, name, value string, masked bool) error {
	body := map[string]any{"name": name, "value": value, "masked": masked}
	return c.post(ctx, "/api/v1/secrets", body, http.StatusNoContent, nil)
}

// GetSecret fetches one row including its value. Returns
// store.ErrNotFound when the secret doesn't exist.
func (c *Client) GetSecret(ctx context.Context, name string) (*Secret, error) {
	u := fmt.Sprintf("%s/api/v1/secrets/%s", c.baseURL, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
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
		return nil, store.ErrNotFound
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
	resp, err := c.http.Do(req)
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
	u := fmt.Sprintf("%s/api/v1/secrets/%s", c.baseURL, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		return store.ErrNotFound
	default:
		return readHTTPError(resp)
	}
}
