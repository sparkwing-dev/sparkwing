package cluster

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/tokenpark"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: a Retry-After the server rounded to nothing would otherwise spin a
// heartbeat loop, so every shed beat waits at least this long.
const minShedBackoff = time.Second

// ErrCredentialChanged ends a claim loop parked on a dead token whose
// configuration was rewritten, so the caller reloads it and starts again.
var ErrCredentialChanged = errors.New("the credential's configuration changed while the token was dead")

const (
	remedyAgentConfig = "Re-enroll this machine with `sparkwing cluster runners add --force`; " +
		"the agent reloads its config within seconds of the rewrite"
	remedyFlagToken = "Restart this process with a live token (--token or SPARKWING_AGENT_TOKEN)"
)

type claimPacing struct {
	logger  *slog.Logger
	prefix  string
	token   string
	remedy  string
	changed func() bool
	pacer   *client.Pacer
	shed    *client.ShedLog
}

func newClaimPacing(logger *slog.Logger, prefix, token string, poll time.Duration, changed func() bool) *claimPacing {
	remedy := remedyFlagToken
	if changed != nil {
		remedy = remedyAgentConfig
	}
	return &claimPacing{
		logger: logger, prefix: prefix, token: token, remedy: remedy, changed: changed,
		pacer: client.NewPacer(poll), shed: client.NewShedLog(client.ShedWarnInterval),
	}
}

func (c *claimPacing) succeeded() {
	if c.pacer.Success() {
		c.logger.Info(c.prefix+"token accepted again; resuming claims", "token_prefix", client.TokenPrefix(c.token))
	}
}

// safety: only a parked loop watches the configuration, so a rewrite during an
// ordinary backoff does not tear down a loop whose token still works.
func (c *claimPacing) failed(ctx context.Context, err error, observe func(client.Pace)) (reload bool) {
	pace := c.pacer.Failure(err)
	if observe != nil {
		observe(pace)
	}
	switch {
	case pace.Dead != nil:
		c.logger.Error(c.prefix+pace.Dead.Explain(c.token, c.remedy),
			"token_prefix", client.TokenPrefix(c.token), "token_state", pace.Dead.State,
			"retry_in", pace.Wait.Round(time.Minute))
	case pace.Parked:
		c.logger.Warn(c.prefix+"token is still refused; staying parked",
			"token_prefix", client.TokenPrefix(c.token), "err", err, "retry_in", pace.Wait.Round(time.Minute))
	case pace.Shed:
		c.logger.Debug(c.prefix+"claim shed by the controller; backing off", "err", err, "retry_after", pace.Wait)
		if c.shed.Due() {
			c.logger.Warn(c.prefix+"controller is shedding claims; polling more slowly", "err", err, "retry_after", pace.Wait)
		}
	default:
		c.logger.Error(c.prefix+"claim failed", "err", err, "retry_in", pace.Wait.Round(time.Millisecond))
	}
	changed := c.changed
	if !pace.Parked {
		changed = nil
	}
	return tokenpark.Wait(ctx, pace.Wait, changed)
}
