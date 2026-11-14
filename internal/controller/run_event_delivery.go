package controller

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/buildinfo"
	"github.com/sparkwing-dev/sparkwing/internal/runevents/webhook"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

const (
	runEventDeliverySlots = 8
	runEventDeliveryTick  = time.Second
)

// safety: runs in its own loop, never the reaper's, so a slow receiver cannot delay reaping or scheduling.
type runEventDeliverer struct {
	operator  *store.Operator
	cipher    Cipher
	sender    *webhook.Sender
	userAgent string
	now       func() time.Time
	jitter    func(n int64) int64
	logger    *slog.Logger
	slots     chan struct{}
	inflight  sync.WaitGroup
}

func newRunEventDeliverer(operator *store.Operator, cipher Cipher, policy webhook.Policy, logger *slog.Logger) *runEventDeliverer {
	return &runEventDeliverer{
		operator:  operator,
		cipher:    cipher,
		sender:    webhook.NewSender(policy),
		userAgent: webhook.UserAgent(buildinfo.Read("sparkwing-controller", "").Version),
		now:       time.Now,
		jitter:    rand.Int64N,
		logger:    logger,
		slots:     make(chan struct{}, runEventDeliverySlots),
	}
}

// DeliverRunEvents sends due run event deliveries until ctx ends. The local
// daemon runs it beside its other workers; a controller starts it from Run.
func (s *Server) DeliverRunEvents(ctx context.Context) error {
	if s.operator == nil || s.secretsCipher == nil {
		<-ctx.Done()
		return nil
	}
	d := newRunEventDeliverer(s.operator, s.secretsCipher, webhook.Policy{Local: s.dashboard != nil && s.dashboard.Local}, s.logger)
	if s.dashboard != nil && s.dashboard.Version != "" {
		d.userAgent = webhook.UserAgent(s.dashboard.Version)
	}
	return d.run(ctx)
}

func (d *runEventDeliverer) run(ctx context.Context) error {
	ticker := time.NewTicker(runEventDeliveryTick)
	defer ticker.Stop()
	defer d.inflight.Wait()
	for {
		if err := d.tick(ctx); err != nil && ctx.Err() == nil {
			d.logger.Warn("run event delivery claim failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// perf: claims only free slots and returns without waiting, so one slow attempt holds a slot, never the loop.
func (d *runEventDeliverer) tick(ctx context.Context) error {
	free := cap(d.slots) - len(d.slots)
	if free == 0 {
		return nil
	}
	claimed, err := d.operator.ClaimRunEventDeliveries(ctx, free)
	if err != nil {
		return err
	}
	for _, row := range claimed {
		d.slots <- struct{}{}
		d.inflight.Go(func() {
			defer func() { <-d.slots }()
			d.deliver(ctx, row)
		})
	}
	return nil
}

func (d *runEventDeliverer) deliver(ctx context.Context, row store.RunEventDelivery) {
	result := webhook.Result{Code: webhook.CodeSecret}
	if secret, err := OpenRunEventSecret(d.cipher, row.Team, row.Subscription); err == nil {
		result = d.sender.Send(ctx, webhook.Attempt{URL: row.Subscription.URL, EventID: row.EventID, Body: row.Body, Secret: secret, UserAgent: d.userAgent, At: d.now()})
	}
	if ctx.Err() != nil {
		// safety: a send cut short by shutdown has no outcome; the lease lapses and another sender retries it.
		return
	}
	outcome := store.RunEventOutcome{Delivered: result.Delivered, Status: result.Status, Code: result.Code}
	if !result.Delivered {
		outcome.Delay = webhook.Delay(row.Attempts, result, d.jitter)
	}
	if _, err := d.operator.FinishRunEventDelivery(ctx, row, outcome); err != nil && ctx.Err() == nil {
		d.logger.Warn("run event delivery outcome not recorded", "team", row.Team, "subscription", row.Subscription.ID, "event", row.EventID, "err", err)
	}
}
