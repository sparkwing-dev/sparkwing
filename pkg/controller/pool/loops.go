package pool

import (
	"context"
	"log"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"k8s.io/client-go/kubernetes"
)

// ReconcileLoop runs periodically to ensure pool size and reclaim abandoned PVCs.
func ReconcileLoop(ctx context.Context, client kubernetes.Interface, p *Pool, ns string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		cfg := LoadConfig(ctx, client, ns)
		start := time.Now()
		if err := p.Reconcile(ctx, cfg.HeartbeatTimeout, cfg.StartupGrace); err != nil {
			log.Printf("pool: reconcile error: %v", err)
		}
		if poolReconcileDur != nil {
			poolReconcileDur.Record(ctx, time.Since(start).Seconds())
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// WarmingLoop picks the stalest dirty PVC and rewarms it, then sleeps.
func WarmingLoop(ctx context.Context, client kubernetes.Interface, p *Pool, ns string) {
	for {
		if ctx.Err() != nil {
			return
		}

		cfg := LoadConfig(ctx, client, ns)
		if len(cfg.WarmImages) == 0 {
			log.Printf("pool: no warm images configured; sleeping 60s")
			SleepOrDone(ctx, 60*time.Second)
			continue
		}

		pvcName, err := p.NextToWarm(ctx, cfg.RefreshInterval)
		if err != nil {
			log.Printf("pool: warning: finding PVC to warm: %v", err)
			SleepOrDone(ctx, 30*time.Second)
			continue
		}
		if pvcName == "" {
			SleepOrDone(ctx, 30*time.Second)
			continue
		}

		log.Printf("pool: warming %s (%d images)", pvcName, len(cfg.WarmImages))
		if err := p.MarkWarming(ctx, pvcName); err != nil {
			log.Printf("pool: warning: marking warming: %v", err)
			SleepOrDone(ctx, 10*time.Second)
			continue
		}

		warmStart := time.Now()
		if err := WarmPVC(ctx, client, ns, pvcName, cfg.WarmImages); err != nil {
			log.Printf("pool: warning: warming %s failed: %v - reverting to dirty", pvcName, err)
			if poolWarmDur != nil {
				poolWarmDur.Record(ctx, time.Since(warmStart).Seconds(),
					metric.WithAttributes(attribute.String("result", "failed")))
			}
			if rerr := p.MarkDirty(ctx, pvcName); rerr != nil {
				log.Printf("pool: warning: failed to revert %s to dirty: %v", pvcName, rerr)
			}
			SleepOrDone(ctx, 30*time.Second)
			continue
		}

		if poolWarmDur != nil {
			poolWarmDur.Record(ctx, time.Since(warmStart).Seconds(),
				metric.WithAttributes(attribute.String("result", "success")))
		}

		if err := p.MarkClean(ctx, pvcName); err != nil {
			log.Printf("pool: marking clean: %v", err)
		}
		log.Printf("pool: %s warmed and returned to pool", pvcName)
	}
}
