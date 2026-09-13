// GitHub webhook bindings against the controller.
package client

import (
	"context"
	"net/http"
	"net/url"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// ConnectGitHubWebhook stores the signing secret one repository's
// deliveries to one pipeline carry, and returns the URL GitHub must post
// them to. The controller keeps the secret; nothing reads it back.
func (c *Client) ConnectGitHubWebhook(
	ctx context.Context, req controller.GitHubWebhookBindingRequest,
) (*controller.GitHubWebhookBindingResponse, error) {
	var out controller.GitHubWebhookBindingResponse
	if err := c.post(ctx, "/api/v1/webhooks/github/bindings", req, http.StatusCreated, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DisconnectGitHubWebhook removes one repository's binding to one
// pipeline and reports whether a binding was there to remove.
func (c *Client) DisconnectGitHubWebhook(
	ctx context.Context, pipeline, repo string,
) (*controller.GitHubWebhookDisconnectResponse, error) {
	q := url.Values{"pipeline": []string{pipeline}, "repo": []string{repo}}
	var out controller.GitHubWebhookDisconnectResponse
	if err := c.deleteJSON(ctx,
		"/api/v1/webhooks/github/bindings?"+q.Encode(), http.StatusOK, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
