package client

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// APIBaseURL addresses HTTP requests carried by the private controller socket.
const APIBaseURL = "http://sparkwing-api"

// NewAPIClient carries HTTP over the private socket and refuses redirects.
func NewAPIClient(socket string) *http.Client {
	const requestTimeout = 45 * time.Second
	return &http.Client{
		Timeout: requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return wingd.DialSocket(ctx, socket, 5*time.Second)
			},
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   16,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: requestTimeout,
		},
	}
}
