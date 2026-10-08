package launcher

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ClaimLease is the lease the launcher takes and never renews: a pod that
// never starts lets its claim lapse, and the node is claimed again.
const ClaimLease = store.MaxLeaseDuration

// Launcher turns the controller's ready nodes into Jobs.
type Launcher struct {
	Kube   kubernetes.Interface
	Ctrl   *client.Client
	Config Config
	Holder string
	Poll   time.Duration
	Logger *slog.Logger

	// safety: LaunchOne adds each Job it creates, Sync rebuilds the set from
	// the Jobs and pods it lists, and Run reads it before every claim, all on
	// Run's goroutine, so it needs no lock.
	pending map[string]bool
}

// Run claims and launches until ctx ends. An idle queue is polled every
// l.Poll; a claim is followed at once by the next. Every [SyncInterval] it
// reconciles its Jobs, and it claims nothing while [MaxPendingJobs] of them
// are not yet running. A failed claim backs off exponentially, and a revoked
// or expired token parks the launcher, asking once an hour, until a claim is
// answered.
func (l *Launcher) Run(ctx context.Context) error {
	if err := l.Config.Validate(); err != nil {
		return err
	}
	var lastSync time.Time
	braked := false
	pacer := client.NewPacer(l.Poll)
	for ctx.Err() == nil {
		if time.Since(lastSync) >= SyncInterval {
			if err := l.Sync(ctx); err != nil && ctx.Err() == nil {
				l.Logger.Warn("launcher: sync failed", "err", err)
			}
			lastSync = time.Now()
		}
		if len(l.pending) >= MaxPendingJobs {
			if !braked {
				l.Logger.Warn("launcher: Jobs are not yet running; pausing claims", "pending", len(l.pending))
			}
			braked = true
			select {
			case <-ctx.Done():
			case <-time.After(l.Poll):
			}
			continue
		}
		braked = false
		launched, err := l.LaunchOne(ctx)
		wait := l.Poll
		switch {
		case ctx.Err() != nil:
		case err != nil && !launched:
			wait = l.claimFailed(pacer, err)
		default:
			if pacer.Success() {
				l.Logger.Info("launcher: token accepted again; resuming claims")
			}
			if err != nil {
				l.Logger.Warn("launcher: launch failed", "err", err)
			}
		}
		if launched && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	return nil
}

func (l *Launcher) claimFailed(pacer *client.Pacer, err error) time.Duration {
	pace := pacer.Failure(err)
	token := l.Ctrl.Token()
	switch {
	case pace.Dead != nil:
		l.Logger.Error("launcher: "+pace.Dead.Explain(token,
			"Restart the launcher with a live claims.launch token in agent-token under --credentials-dir"),
			"token_prefix", client.TokenPrefix(token), "token_state", pace.Dead.State)
	case pace.Parked:
		l.Logger.Warn("launcher: token is still refused; staying parked",
			"token_prefix", client.TokenPrefix(token), "err", err, "retry_in", pace.Wait.Round(time.Minute))
	default:
		l.Logger.Warn("launcher: claim failed", "err", err, "retry_in", pace.Wait.Round(time.Millisecond))
	}
	return pace.Wait
}

// LaunchOne claims one ready node and creates its Job. It reports whether it
// claimed one. A Job that cannot be created leaves the claim to lapse, and the
// node is claimed again at a later generation.
func (l *Launcher) LaunchOne(ctx context.Context) (bool, error) {
	claim, err := l.Ctrl.ClaimLaunch(ctx, l.Holder, ClaimLease, l.Config.Deadline)
	if err != nil || claim == nil {
		return false, err
	}
	// safety: a token too short to outlast the margin would start a Job that
	// dies before its pod runs, so the claim is left to lapse instead.
	if time.Duration(claim.LifetimeSecs)*time.Second < store.MinLaunchLifetime {
		return true, fmt.Errorf("claim %s/%s lives %ds, under the %s floor; no Job created",
			claim.RunID, claim.NodeID, claim.LifetimeSecs, store.MinLaunchLifetime)
	}
	job := BuildJob(l.Config, *claim)
	_, err = l.Kube.BatchV1().Jobs(l.Config.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		err = nil
	}
	if err == nil {
		if l.pending == nil {
			l.pending = map[string]bool{}
		}
		l.pending[job.Name] = true
		l.Logger.Info("launcher: job created", "job", job.Name, "team", claim.Team,
			"run_id", claim.RunID, "node_id", claim.NodeID, "generation", claim.Generation)
	}
	return true, err
}
