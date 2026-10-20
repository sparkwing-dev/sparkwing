package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	client, err := dockerClient(dockerHost)
	if err != nil {
		return err
	}
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
			return fmt.Errorf("Docker at %s was not usable within %s: %w", dockerHost, within, last)
		case <-ticker.C:
		}
	}
}

func dockerClient(dockerHost string) (*http.Client, error) {
	socket, ok := strings.CutPrefix(dockerHost, "unix://")
	if !ok || socket == "" {
		return nil, fmt.Errorf("Cloud Jobs need a unix DOCKER_HOST, got %q", dockerHost)
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &http.Client{Timeout: 2 * time.Second, Transport: transport}, nil
}

// safety: API failure must still stop the daemon within the shared administrative kill budget.
func stopDocker(ctx context.Context, client *http.Client, stopDaemon func() error) (err error) {
	defer func() { err = errors.Join(err, stopDaemon()) }()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker container listing answered %d", response.StatusCode)
	}
	var containers []struct {
		ID string `json:"Id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&containers); err != nil {
		return err
	}
	for _, container := range containers {
		request, e := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker/containers/"+url.PathEscape(container.ID)+"/kill?signal=KILL", nil)
		if e != nil {
			err = errors.Join(err, e)
			continue
		}
		response, e := client.Do(request)
		if e != nil {
			err = errors.Join(err, e)
			continue
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusConflict {
			err = errors.Join(err, fmt.Errorf("Docker container kill answered %d", response.StatusCode))
		}
	}
	return err
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
