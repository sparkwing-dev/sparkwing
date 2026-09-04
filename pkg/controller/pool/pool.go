package pool

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	StateClean   = "clean"
	StateInUse   = "in-use"
	StateDirty   = "dirty"
	StateWarming = "warming"
)

// PVC annotations used for pool tracking. The PVC itself is the source of truth.
const (
	AnnPoolState    = "sparkwing.dev/pool-state"
	AnnWarmedAt     = "sparkwing.dev/warmed-at"
	AnnCheckedOutBy = "sparkwing.dev/checked-out-by"
	AnnCheckedOutAt = "sparkwing.dev/checked-out-at"
	AnnHeartbeatAt  = "sparkwing.dev/heartbeat-at"
	AnnPoolMember   = "sparkwing.dev/pool-member"
	PoolLabelKey    = "sparkwing.dev/pool"
	PoolLabelValue  = "cache"
)

const memberPrefix = "sparkwing-cache-pool-"

// Pool manages a pool of warm Docker cache PVCs.
type Pool struct {
	Client    kubernetes.Interface
	Namespace string
	poolSize  int
	pvcSize   string
}

func NewPool(client kubernetes.Interface, namespace string, poolSize int, pvcSize string) *Pool {
	if pvcSize == "" {
		pvcSize = "20Gi"
	}
	if poolSize <= 0 {
		poolSize = 2
	}
	return &Pool{
		Client:    client,
		Namespace: namespace,
		poolSize:  poolSize,
		pvcSize:   pvcSize,
	}
}

// Reconcile ensures the pool has the configured number of PVCs and
// reclaims abandoned in-use PVCs based on heartbeat age. Falls back to
// checked-out-at + startupGrace when no heartbeat was ever recorded so
// brand-new checkouts aren't reclaimed before the runner's first beat.
func (p *Pool) Reconcile(ctx context.Context, heartbeatTimeout, startupGrace time.Duration) error {
	pvcs, err := p.list(ctx)
	if err != nil {
		return fmt.Errorf("listing pool pvcs: %w", err)
	}

	// safety: a deleted middle member leaves a free index the member count never lands on
	occupied := occupiedMembers(pvcs)
	next := 0
	for have := len(pvcs); have < p.poolSize; have++ {
		for occupied[next] {
			next++
		}
		occupied[next] = true
		created, err := p.create(ctx, next)
		if err != nil {
			log.Printf("pool: warning: creating pool PVC %d: %v", next, err)
			continue
		}
		if !created {
			log.Printf("pool: pool PVC %s already exists", memberName(next))
			continue
		}
		log.Printf("pool: created %s", memberName(next))
	}

	now := time.Now()
	for _, pvc := range pvcs {
		if pvc.Annotations[AnnPoolState] != StateInUse {
			continue
		}

		var lastContact time.Time
		var source string
		if s := pvc.Annotations[AnnHeartbeatAt]; s != "" {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				lastContact = t
				source = "heartbeat"
			}
		}
		if lastContact.IsZero() {
			// safety: no heartbeat yet; use checked-out-at with startupGrace to avoid premature reclaim
			if s := pvc.Annotations[AnnCheckedOutAt]; s != "" {
				if t, err := time.Parse(time.RFC3339, s); err == nil {
					lastContact = t
					source = "checked-out-at (no heartbeat yet)"
				}
			}
		}
		if lastContact.IsZero() {
			continue
		}

		age := now.Sub(lastContact)
		threshold := heartbeatTimeout
		if source == "checked-out-at (no heartbeat yet)" {
			threshold = startupGrace
		}

		if age > threshold {
			// safety: the owner annotation is the job id its caller supplied, so %q escapes it.
			log.Printf("pool: reclaiming abandoned PVC %s (%s %s ago, owned by %q)",
				pvc.Name, source, age.Round(time.Second),
				pvc.Annotations[AnnCheckedOutBy])
			if err := p.setState(ctx, pvc.Name, StateDirty, map[string]string{
				AnnCheckedOutBy: "",
				AnnCheckedOutAt: "",
				AnnHeartbeatAt:  "",
			}); err != nil {
				log.Printf("pool: warning: reclaiming PVC: %v", err)
			}
		}
	}
	return nil
}

// Heartbeat refreshes the heartbeat timestamp on a checked-out PVC.
// Returns an error when the caller no longer owns the PVC.
func (p *Pool) Heartbeat(ctx context.Context, pvcName, jobID string) error {
	pvc, err := p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pvc.Annotations[AnnPoolState] != StateInUse {
		return fmt.Errorf("pvc %s is not in-use (state=%s)", pvcName, pvc.Annotations[AnnPoolState])
	}
	if pvc.Annotations[AnnCheckedOutBy] != jobID {
		return fmt.Errorf("pvc %s owned by %s, not %s", pvcName, pvc.Annotations[AnnCheckedOutBy], jobID)
	}
	pvc.Annotations[AnnHeartbeatAt] = time.Now().UTC().Format(time.RFC3339)
	_, err = p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Update(ctx, pvc, metav1.UpdateOptions{})
	return err
}

// List returns all pool PVCs sorted by name.
func (p *Pool) List(ctx context.Context) ([]corev1.PersistentVolumeClaim, error) {
	return p.list(ctx)
}

func (p *Pool) list(ctx context.Context) ([]corev1.PersistentVolumeClaim, error) {
	pvcs, err := p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: PoolLabelKey + "=" + PoolLabelValue,
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pvcs.Items, func(i, j int) bool {
		return pvcs.Items[i].Name < pvcs.Items[j].Name
	})
	return pvcs.Items, nil
}

func occupiedMembers(pvcs []corev1.PersistentVolumeClaim) map[int]bool {
	occupied := make(map[int]bool, len(pvcs))
	for _, pvc := range pvcs {
		if i, ok := memberIndex(pvc); ok {
			occupied[i] = true
		}
	}
	return occupied
}

func memberIndex(pvc corev1.PersistentVolumeClaim) (int, bool) {
	if s := pvc.Annotations[AnnPoolMember]; s != "" {
		if i, err := strconv.Atoi(s); err == nil && i >= 0 {
			return i, true
		}
	}
	// safety: pools created before the member annotation carry the index only in the name
	s, ok := strings.CutPrefix(pvc.Name, memberPrefix)
	if !ok {
		return 0, false
	}
	i, err := strconv.Atoi(s)
	if err != nil || i < 0 {
		return 0, false
	}
	return i, true
}

func memberName(index int) string {
	return fmt.Sprintf("%s%d", memberPrefix, index)
}

func (p *Pool) create(ctx context.Context, index int) (bool, error) {
	name := memberName(index)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: p.Namespace,
			Labels: map[string]string{
				PoolLabelKey:            PoolLabelValue,
				"app":                   "sparkwing-cache-pool",
				"sparkwing.dev/managed": "pool-manager",
			},
			Annotations: map[string]string{
				AnnPoolState:  StateDirty,
				AnnPoolMember: strconv.Itoa(index),
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(p.pvcSize),
				},
			},
		},
	}
	_, err := p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Checkout atomically allocates a clean PVC for a job. Returns "" if
// no clean PVC is available.
func (p *Pool) Checkout(ctx context.Context, jobID string) (string, error) {
	pvcs, err := p.list(ctx)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, pvc := range pvcs {
		if pvc.Annotations[AnnPoolState] != StateClean {
			continue
		}
		// safety: optimistic resourceVersion prevents double-checkout under concurrent claims
		pvc.Annotations[AnnPoolState] = StateInUse
		pvc.Annotations[AnnCheckedOutBy] = jobID
		pvc.Annotations[AnnCheckedOutAt] = now
		pvc.Annotations[AnnHeartbeatAt] = now
		_, err := p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Update(ctx, &pvc, metav1.UpdateOptions{})
		if err != nil {
			if errors.IsConflict(err) {
				continue
			}
			return "", err
		}
		// safety: %q escapes a caller-supplied job id that would otherwise forge a log line.
		log.Printf("pool: checked out PVC %s for job %q", pvc.Name, jobID)
		return pvc.Name, nil
	}
	return "", nil
}

// Return marks a PVC as clean (ready for the next checkout) after a
// job finishes. We don't mark it dirty here -- the cache is still
// mostly valid; periodic age-based rewarming handles staleness. The
// warmed-at timestamp is preserved so the refresher can see how long
// ago the last full warm was.
func (p *Pool) Return(ctx context.Context, pvcName string) error {
	return p.setState(ctx, pvcName, StateClean, map[string]string{
		AnnCheckedOutBy: "",
		AnnCheckedOutAt: "",
		AnnHeartbeatAt:  "",
	})
}

// NextToWarm returns the PVC most in need of warming, or "" if none.
// Prefers dirty PVCs first, then clean PVCs older than refreshInterval.
func (p *Pool) NextToWarm(ctx context.Context, refreshInterval time.Duration) (string, error) {
	pvcs, err := p.list(ctx)
	if err != nil {
		return "", err
	}

	type candidate struct {
		name     string
		warmedAt time.Time
		dirty    bool
	}
	var candidates []candidate
	now := time.Now()
	for _, pvc := range pvcs {
		state := pvc.Annotations[AnnPoolState]
		var t time.Time
		if s := pvc.Annotations[AnnWarmedAt]; s != "" {
			t, _ = time.Parse(time.RFC3339, s)
		}
		switch state {
		case StateDirty, "":
			candidates = append(candidates, candidate{name: pvc.Name, warmedAt: t, dirty: true})
		case StateClean:
			if now.Sub(t) > refreshInterval {
				candidates = append(candidates, candidate{name: pvc.Name, warmedAt: t, dirty: false})
			}
		}
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dirty != candidates[j].dirty {
			return candidates[i].dirty
		}
		return candidates[i].warmedAt.Before(candidates[j].warmedAt)
	})
	return candidates[0].name, nil
}

func (p *Pool) setState(ctx context.Context, name, state string, clearAnnotations map[string]string) error {
	pvc, err := p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if pvc.Annotations == nil {
		pvc.Annotations = map[string]string{}
	}
	pvc.Annotations[AnnPoolState] = state
	for k, v := range clearAnnotations {
		if v == "" {
			delete(pvc.Annotations, k)
		} else {
			pvc.Annotations[k] = v
		}
	}
	_, err = p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Update(ctx, pvc, metav1.UpdateOptions{})
	return err
}

// MarkWarming transitions a PVC from dirty to warming.
func (p *Pool) MarkWarming(ctx context.Context, name string) error {
	return p.setState(ctx, name, StateWarming, nil)
}

// MarkClean transitions a PVC from warming to clean after a successful rewarm.
// Updates warmed-at to now so the age-based refresher knows it's fresh.
func (p *Pool) MarkClean(ctx context.Context, name string) error {
	return p.setState(ctx, name, StateClean, map[string]string{
		AnnWarmedAt: time.Now().UTC().Format(time.RFC3339),
	})
}

// MarkDirty reverts a PVC back to dirty so a failed warm doesn't
// leave it permanently stuck in "warming".
func (p *Pool) MarkDirty(ctx context.Context, name string) error {
	return p.setState(ctx, name, StateDirty, nil)
}

// ReturnFor is like Return but validates that the caller owns the
// PVC, preventing one job from returning another's PVC.
func (p *Pool) ReturnFor(ctx context.Context, pvcName, jobID string) error {
	pvc, err := p.Client.CoreV1().PersistentVolumeClaims(p.Namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	owner := pvc.Annotations[AnnCheckedOutBy]
	if owner != "" && owner != jobID {
		return fmt.Errorf("pvc %s owned by %s, not %s", pvcName, owner, jobID)
	}
	return p.Return(ctx, pvcName)
}
