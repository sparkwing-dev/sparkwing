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
	// Capacity reports why the fleet can start no machine for another Job,
	// or "" when it can. Nil reads the fleet's capacity from its Jobs alone.
	Capacity func(context.Context) (string, error)
}

// Run claims and launches until ctx ends. An idle queue is polled every
// l.Poll; a claim is followed at once by the next. Every [SyncInterval] it
// reconciles its Jobs, and while the fleet has no capacity it claims nothing,
// so ready nodes wait in the controller's queue unbilled.
func (l *Launcher) Run(ctx context.Context) error {
	if err := l.Config.Validate(); err != nil {
		return err
	}
	var lastSync time.Time
	var waiting string
	for ctx.Err() == nil {
		if time.Since(lastSync) >= SyncInterval {
			var err error
			if waiting, err = l.Sync(ctx); err != nil && ctx.Err() == nil {
				l.Logger.Warn("launcher: sync failed", "err", err)
			}
			lastSync = time.Now()
		}
		if waiting != "" {
			select {
			case <-ctx.Done():
			case <-time.After(l.Poll):
			}
			continue
		}
		launched, err := l.LaunchOne(ctx)
		if err != nil && ctx.Err() == nil {
			l.Logger.Warn("launcher: launch failed", "err", err)
		}
		if launched && err == nil {
			continue
		}
		select {
		case <-ctx.Done():
		case <-time.After(l.Poll):
		}
	}
	return nil
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
		l.Logger.Info("launcher: job created", "job", job.Name, "team", claim.Team,
			"run_id", claim.RunID, "node_id", claim.NodeID, "generation", claim.Generation)
	}
	return true, err
}
