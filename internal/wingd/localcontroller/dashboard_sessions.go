package localcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

var _ wingd.DashboardSessionStore = (*localController)(nil)

func (c *localController) MintDashboardCode(ctx context.Context, digest, origin string, peer localhostwire.LocalPeerIdentity, now, expires time.Time) error {
	encoded, err := json.Marshal(peer)
	if err != nil {
		return err
	}
	return c.operator.MintLocalDashboardCode(ctx, digest, origin, string(encoded), now, expires)
}

func (c *localController) RedeemDashboardCode(ctx context.Context, code, session, origin string, now, expires time.Time) (localhostwire.LocalPeerIdentity, error) {
	encoded, err := c.operator.RedeemLocalDashboardCode(ctx, code, session, origin, now, expires)
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, err
	}
	var peer localhostwire.LocalPeerIdentity
	return peer, json.Unmarshal([]byte(encoded), &peer)
}

func (c *localController) DashboardSession(ctx context.Context, digest string, now time.Time) (localhostwire.LocalPeerIdentity, string, time.Time, bool, error) {
	session, err := c.operator.LocalDashboardSessionAt(ctx, digest, now)
	if errors.Is(err, store.ErrNotFound) {
		return localhostwire.LocalPeerIdentity{}, "", time.Time{}, false, nil
	}
	if err != nil {
		return localhostwire.LocalPeerIdentity{}, "", time.Time{}, false, err
	}
	var peer localhostwire.LocalPeerIdentity
	if err := json.Unmarshal([]byte(session.Peer), &peer); err != nil {
		return localhostwire.LocalPeerIdentity{}, "", time.Time{}, false, err
	}
	return peer, session.Origin, session.ExpiresAt, true, nil
}

func (c *localController) RevokeDashboardSession(ctx context.Context, digest string, now time.Time) (bool, error) {
	return c.operator.RevokeLocalDashboardSession(ctx, digest, now)
}
