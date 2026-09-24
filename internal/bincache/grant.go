package bincache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ErrNoCacheGrant reports a controller that mints no cache grants: one that
// predates the route or holds no cache grant key to sign with. The run proceeds
// without the binary and dependency caches.
var ErrNoCacheGrant = errors.New("controller mints no cache grants")

// RequestCacheGrant asks the controller for a grant that opens the cache to
// runID's team, authenticating with the runner's own token. The grant is the
// only cache credential a runner hands its run.
func RequestCacheGrant(ctx context.Context, controllerURL, runnerToken, runID string) (string, error) {
	if controllerURL == "" || runID == "" {
		return "", ErrNoCacheGrant
	}
	endpoint := strings.TrimRight(controllerURL, "/") + "/api/v1/runs/" + neturl.PathEscape(runID) + "/cache-grant"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return "", err
	}
	if runnerToken != "" {
		req.Header.Set("Authorization", "Bearer "+runnerToken)
	}
	if fence, ok := store.NodeClaimFenceFromContext(ctx); ok {
		req.Header.Set(store.ClaimHolderHeader, fence.HolderID)
		req.Header.Set(store.ClaimMembershipHeader, fence.MembershipID)
		req.Header.Set(store.ClaimReservationHeader, fence.ReservationID)
		req.Header.Set(store.ClaimGenerationHeader, strconv.FormatInt(fence.ClaimGeneration, 10))
	} else if fence, ok := store.TriggerClaimFenceFromContext(ctx); ok {
		req.Header.Set(store.TriggerGenerationHeader, strconv.FormatInt(fence.ClaimGeneration, 10))
	}
	cli := &http.Client{
		Timeout: 15 * time.Second,
		// safety: a redirect would carry the runner token to whatever origin it names.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := cli.Do(req)
	if err != nil {
		return "", fmt.Errorf("request cache grant: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return "", ErrNoCacheGrant
	case resp.StatusCode != http.StatusOK:
		msg, err := io.ReadAll(io.LimitReader(resp.Body, 512))
		if err != nil {
			return "", fmt.Errorf("request cache grant: %s", resp.Status)
		}
		return "", fmt.Errorf("request cache grant: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var body struct {
		Grant string `json:"grant"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("request cache grant: decode: %w", err)
	}
	// safety: anything else in this field would be sent to the cache as a bearer,
	// and a controller that answered with the operator token would hand it to team code.
	if !strings.HasPrefix(body.Grant, authwire.CacheGrantPrefix) {
		return "", errors.New("request cache grant: the controller answered with no grant")
	}
	return body.Grant, nil
}
