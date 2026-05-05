package pool

import (
	"context"
	"log"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const (
	ConfigMapName = "sparkwing-cache-config"
	DefaultNS     = "sparkwing"
)

// Config is the cache manager's runtime config, loaded from a ConfigMap.
type Config struct {
	WarmImages       []string
	RefreshInterval  time.Duration
	HeartbeatTimeout time.Duration
	StartupGrace     time.Duration
	PoolSize         int
	PVCSize          string
	Namespace        string
}

// rawConfig is the wire format from the ConfigMap - durations as strings.
type rawConfig struct {
	WarmImages       []string `json:"warm_images"`
	RefreshInterval  string   `json:"refresh_interval"`
	HeartbeatTimeout string   `json:"heartbeat_timeout"`
	StartupGrace     string   `json:"startup_grace"`
	PoolSize         int      `json:"pool_size"`
	PVCSize          string   `json:"pvc_size"`
}

// LoadConfig reads the pool-manager config from a ConfigMap.
// Returns sensible defaults if the ConfigMap is missing.
func LoadConfig(ctx context.Context, client kubernetes.Interface, namespace string) *Config {
	mgr := &Config{
		RefreshInterval:  1 * time.Hour,
		HeartbeatTimeout: 5 * time.Minute,
		StartupGrace:     2 * time.Minute,
		PoolSize:         2,
		PVCSize:          "20Gi",
		Namespace:        namespace,
	}

	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, ConfigMapName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return mgr
		}
		log.Printf("pool: warning: reading ConfigMap: %v", err)
		return mgr
	}

	if data := cm.Data["config.yaml"]; data != "" {
		var raw rawConfig
		if err := yaml.Unmarshal([]byte(data), &raw); err != nil {
			log.Printf("pool: warning: parsing config.yaml: %v", err)
			return mgr
		}
		if len(raw.WarmImages) > 0 {
			mgr.WarmImages = raw.WarmImages
		}
		if raw.RefreshInterval != "" {
			if d, err := time.ParseDuration(raw.RefreshInterval); err == nil {
				mgr.RefreshInterval = d
			}
		}
		if raw.HeartbeatTimeout != "" {
			if d, err := time.ParseDuration(raw.HeartbeatTimeout); err == nil {
				mgr.HeartbeatTimeout = d
			}
		}
		if raw.StartupGrace != "" {
			if d, err := time.ParseDuration(raw.StartupGrace); err == nil {
				mgr.StartupGrace = d
			}
		}
		if raw.PoolSize > 0 {
			mgr.PoolSize = raw.PoolSize
		}
		if raw.PVCSize != "" {
			mgr.PVCSize = raw.PVCSize
		}
	}
	return mgr
}

// SleepOrDone sleeps for d or returns early if the context is cancelled.
func SleepOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
