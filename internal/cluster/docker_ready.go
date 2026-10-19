package cluster

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// dockerStartup bounds how long run-node waits for the sidecar, so a
// crash-looping dockerd fails the Job instead of spending its deadline.
const dockerStartup = 3 * time.Minute

// waitForDocker is the Docker sidecar's readiness check, run by the workload
// itself: it returns once DOCKER_HOST answers /_ping for this process's own
// identity, so a socket the workload cannot open never counts as ready.
func waitForDocker(ctx context.Context, dockerHost string, within time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	socket, ok := strings.CutPrefix(dockerHost, "unix://")
	if !ok || socket == "" {
		return fmt.Errorf("Cloud Jobs need a unix DOCKER_HOST, got %q", dockerHost)
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 2 * time.Second, Transport: transport}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		last = ping(ctx, client)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Docker at %s was not usable within %s: %w", socket, within, last)
		case <-ticker.C:
		}
	}
}

func ping(ctx context.Context, client *http.Client) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "OK" {
		return fmt.Errorf("Docker /_ping answered %d %q", response.StatusCode, body)
	}
	return nil
}
