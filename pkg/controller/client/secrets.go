package client

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

// Secret contains the legacy execution secret read response.
type Secret struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	Principal string `json:"principal"`
	Pipeline  string `json:"pipeline,omitempty"`
	Masked    bool   `json:"masked"`
	Shared    bool   `json:"shared,omitempty"`
	Bound     bool   `json:"bound"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// GetSecret fetches one row including its value. Returns
// store.ErrNotFound when the secret doesn't exist.
func (c *Client) GetSecret(ctx context.Context, name string) (*Secret, error) {
	return c.getSecret(ctx, name, "")
}

// GetSecretForRun fetches the row owned by the pipeline of runID. A claim
// token reads only its own run's secrets that the run's plan declares; an
// operator or team-admin credential reads the row by name.
func (c *Client) GetSecretForRun(ctx context.Context, name, runID string) (*Secret, error) {
	return c.getSecret(ctx, name, queryParam("run", runID))
}

// GetSecretForPipeline fetches the row a pipeline owns, falling back to
// the unscoped row. Only an operator or team-admin credential reads by
// name; any other bearer is refused with claim_required.
func (c *Client) GetSecretForPipeline(ctx context.Context, name, pipeline string) (*Secret, error) {
	return c.getSecret(ctx, name, queryParam("pipeline", pipeline))
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
	case http.StatusForbidden:
		return nil, secretRefusal(resp)
	default:
		return nil, readHTTPError(resp)
	}
}

// safety: a refused read carries its reason in the message, such as the
// declaration an undeclared secret is missing, and the code alone would hide
// it from the step that asked.
func secretRefusal(resp *http.Response) error {
	var body struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || body.Message == "" {
		return fmt.Errorf("controller %d: %s", resp.StatusCode, cmp.Or(body.Code, resp.Status))
	}
	return fmt.Errorf("controller %d (%s): %s", resp.StatusCode, body.Code, body.Message)
}

func queryParam(key, value string) string {
	if value == "" {
		return ""
	}
	return "?" + key + "=" + url.QueryEscape(value)
}
