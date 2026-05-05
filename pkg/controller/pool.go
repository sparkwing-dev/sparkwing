package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/pool"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// PoolConfig tells the controller how to run the warm-PVC pool. Pool
// is optional: when PoolConfig is nil the controller omits the pool
// routes and skips the loops entirely. When set, the controller owns
// the pool's lifecycle (reconcile + warming goroutines) for the
// duration of its Serve call.
type PoolConfig struct {
	// Client is the in-cluster kubernetes.Interface. Required.
	Client kubernetes.Interface
	// Namespace the pool manages (where PVCs live). Required.
	Namespace string
	// ReconcileEvery is the reconcile-loop cadence. Zero uses 15s.
	ReconcileEvery time.Duration
}

// AttachPool wires the pool into the server. Returns the server for
// chaining. Must be called before Handler() so the pool routes land
// on the returned mux.
func (s *Server) AttachPool(cfg PoolConfig) *Server {
	if cfg.Namespace == "" || cfg.Client == nil {
		s.logger.Warn("controller: AttachPool called with empty config; skipping pool")
		return s
	}
	if cfg.ReconcileEvery <= 0 {
		cfg.ReconcileEvery = 15 * time.Second
	}
	s.pool = &poolBinding{cfg: cfg}
	return s
}

// poolBinding holds the pool lifecycle state. Lazy-initialized so
// AttachPool doesn't require a live API server.
type poolBinding struct {
	cfg  PoolConfig
	pool *pool.Pool
	pcfg *pool.Config
}

// run spawns the pool's background loops. Blocks until ctx is done.
func (p *poolBinding) run(ctx context.Context, logger *slog.Logger) {
	p.pcfg = pool.LoadConfig(ctx, p.cfg.Client, p.cfg.Namespace)
	p.pool = pool.NewPool(p.cfg.Client, p.cfg.Namespace, p.pcfg.PoolSize, p.pcfg.PVCSize)
	pool.InitMetrics()
	logger.Info("controller pool: starting",
		"namespace", p.cfg.Namespace,
		"pool_size", p.pcfg.PoolSize,
		"pvc_size", p.pcfg.PVCSize,
		"warm_images", len(p.pcfg.WarmImages),
	)

	go p.reconcileLoop(ctx, logger)
	go pool.WarmingLoop(ctx, p.cfg.Client, p.pool, p.cfg.Namespace)
	<-ctx.Done()
}

func (p *poolBinding) reconcileLoop(ctx context.Context, logger *slog.Logger) {
	t := time.NewTicker(p.cfg.ReconcileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := p.pool.Reconcile(ctx, p.pcfg.HeartbeatTimeout, p.pcfg.StartupGrace); err != nil {
				logger.Error("pool reconcile", "err", err)
			}
		}
	}
}

// ready returns true once the background goroutines have loaded
// config and constructed the Pool. Handlers short-circuit to 503
// until then so callers don't see nil-panic mid-startup.
func (p *poolBinding) ready() bool {
	return p != nil && p.pool != nil
}

// --- HTTP handlers ---

func (s *Server) handlePoolList(w http.ResponseWriter, r *http.Request) {
	if !s.pool.ready() {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	list, err := s.pool.pool.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	type entry struct {
		Name         string `json:"name"`
		State        string `json:"state"`
		WarmedAt     string `json:"warmed_at,omitempty"`
		CheckedOutBy string `json:"checked_out_by,omitempty"`
		CheckedOutAt string `json:"checked_out_at,omitempty"`
	}
	out := make([]entry, 0, len(list))
	for _, pvc := range list {
		a := pvc.Annotations
		if a == nil {
			a = map[string]string{}
		}
		out = append(out, entry{
			Name:         pvc.Name,
			State:        a[pool.AnnPoolState],
			WarmedAt:     a[pool.AnnWarmedAt],
			CheckedOutBy: a[pool.AnnCheckedOutBy],
			CheckedOutAt: a[pool.AnnCheckedOutAt],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pool_size": s.pool.pcfg.PoolSize,
		"pvc_size":  s.pool.pcfg.PVCSize,
		"pvcs":      out,
	})
}

func (s *Server) handlePoolCheckout(w http.ResponseWriter, r *http.Request) {
	if !s.pool.ready() {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, errors.New("job_id is required"))
		return
	}
	name, err := s.pool.pool.Checkout(r.Context(), jobID)
	if err != nil {
		// No clean PVC available -> 409 so the caller falls back to a
		// cache-less build instead of erroring.
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pvc": name})
}

func (s *Server) handlePoolReturn(w http.ResponseWriter, r *http.Request) {
	if !s.pool.ready() {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	name := r.URL.Query().Get("pvc")
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("pvc is required"))
		return
	}
	if err := s.pool.pool.Return(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePoolHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.pool.ready() {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	name := r.URL.Query().Get("pvc")
	jobID := r.URL.Query().Get("job_id")
	if name == "" || jobID == "" {
		writeError(w, http.StatusBadRequest, errors.New("pvc and job_id are required"))
		return
	}
	if err := s.pool.pool.Heartbeat(r.Context(), name, jobID); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PoolListForTesting returns the raw PVC list. For tests asserting
// pool state without going through HTTP.
func (s *Server) PoolListForTesting(ctx context.Context) ([]corev1.PersistentVolumeClaim, error) {
	if !s.pool.ready() {
		return nil, errors.New("pool not attached")
	}
	return s.pool.pool.List(ctx)
}

// Compile-time confirmation that json.Marshal stays in scope for any
// future debug endpoint that wants to dump PoolConfig.
var _ = json.Marshal
