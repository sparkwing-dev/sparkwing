package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller/pool"

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
	// WarmerServiceAccount names the ServiceAccount warmer pods run as.
	// Empty uses [pool.WarmerServiceAccountName].
	WarmerServiceAccount string
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
	if cfg.WarmerServiceAccount == "" {
		cfg.WarmerServiceAccount = pool.WarmerServiceAccountName
	}
	s.pool = &poolBinding{cfg: cfg}
	return s
}

type poolBinding struct {
	cfg   PoolConfig
	bound atomic.Pointer[boundPool]
}

type boundPool struct {
	pool *pool.Pool
	pcfg *pool.Config
}

func (p *poolBinding) run(ctx context.Context, logger *slog.Logger) {
	pcfg := pool.LoadConfig(ctx, p.cfg.Client, p.cfg.Namespace)
	// safety: the router is already serving when this runs, so the pool and its
	// config are published as one pointer after both are built; a handler that
	// reads the pointer either sees nothing or sees a finished binding.
	b := &boundPool{
		pool: pool.NewPool(p.cfg.Client, p.cfg.Namespace, pcfg.PoolSize, pcfg.PVCSize),
		pcfg: pcfg,
	}
	p.bound.Store(b)
	pool.InitMetrics()
	logger.Info(
		"controller pool: starting",
		"namespace", p.cfg.Namespace,
		"pool_size", pcfg.PoolSize,
		"pvc_size", pcfg.PVCSize,
		"warm_images", len(pcfg.WarmImages),
	)

	go p.reconcileLoop(ctx, logger, b)
	go pool.WarmingLoop(ctx, p.cfg.Client, b.pool, p.cfg.Namespace, p.cfg.WarmerServiceAccount)
	<-ctx.Done()
}

func (p *poolBinding) reconcileLoop(ctx context.Context, logger *slog.Logger, b *boundPool) {
	t := time.NewTicker(p.cfg.ReconcileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := b.pool.Reconcile(ctx, b.pcfg.HeartbeatTimeout, b.pcfg.StartupGrace); err != nil {
				logger.Error("pool reconcile", "err", err)
			}
		}
	}
}

func (p *poolBinding) binding() *boundPool {
	if p == nil {
		return nil
	}
	return p.bound.Load()
}

func (s *Server) handlePoolList(w http.ResponseWriter, r *http.Request) {
	bound := s.pool.binding()
	if bound == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	list, err := bound.pool.List(r.Context())
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
		"pool_size": bound.pcfg.PoolSize,
		"pvc_size":  bound.pcfg.PVCSize,
		"pvcs":      out,
	})
}

func (s *Server) handlePoolCheckout(w http.ResponseWriter, r *http.Request) {
	bound := s.pool.binding()
	if bound == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	jobID := r.URL.Query().Get("job_id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, errors.New("job_id is required"))
		return
	}
	name, err := bound.pool.Checkout(r.Context(), jobID)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pvc": name})
}

func (s *Server) handlePoolReturn(w http.ResponseWriter, r *http.Request) {
	bound := s.pool.binding()
	if bound == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	name := r.URL.Query().Get("pvc")
	if name == "" {
		writeError(w, http.StatusBadRequest, errors.New("pvc is required"))
		return
	}
	if err := bound.pool.Return(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePoolHeartbeat(w http.ResponseWriter, r *http.Request) {
	bound := s.pool.binding()
	if bound == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("pool not ready"))
		return
	}
	name := r.URL.Query().Get("pvc")
	jobID := r.URL.Query().Get("job_id")
	if name == "" || jobID == "" {
		writeError(w, http.StatusBadRequest, errors.New("pvc and job_id are required"))
		return
	}
	if err := bound.pool.Heartbeat(r.Context(), name, jobID); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var _ = json.Marshal
