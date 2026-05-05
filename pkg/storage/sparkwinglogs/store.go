// Package sparkwinglogs adapts the sparkwing-logs HTTP service to
// storage.LogStore. Thin wrapper around logs.Client.
package sparkwinglogs

import (
	"context"
	"io"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/logs"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
)

// Store implements storage.LogStore on top of logs.Client.
type Store struct {
	client *logs.Client
}

// New constructs a Store talking to the logs service at baseURL.
// Empty token disables auth.
func New(baseURL string, httpClient *http.Client, token string) *Store {
	return &Store{client: logs.NewClientWithToken(baseURL, httpClient, token)}
}

// FromClient wraps an existing logs.Client.
func FromClient(c *logs.Client) *Store { return &Store{client: c} }

var _ storage.LogStore = (*Store)(nil)

func (s *Store) Append(ctx context.Context, runID, nodeID string, data []byte) error {
	return s.client.Append(ctx, runID, nodeID, data)
}

func (s *Store) Read(ctx context.Context, runID, nodeID string, opts storage.ReadOpts) ([]byte, error) {
	return s.client.ReadFiltered(ctx, runID, nodeID, logs.ReadFilter{
		Tail:  opts.Tail,
		Head:  opts.Head,
		Lines: opts.Lines,
		Grep:  opts.Grep,
	})
}

func (s *Store) ReadRun(ctx context.Context, runID string) ([]byte, error) {
	return s.client.ReadRun(ctx, runID)
}

func (s *Store) Stream(ctx context.Context, runID, nodeID string) (io.ReadCloser, error) {
	return s.client.Stream(ctx, runID, nodeID)
}

func (s *Store) DeleteRun(ctx context.Context, runID string) error {
	return s.client.DeleteRun(ctx, runID)
}
