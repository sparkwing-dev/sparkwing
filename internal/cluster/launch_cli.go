package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/sparkwing-dev/sparkwing/internal/runners/k8s"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runLaunchCLI(args []string) error {
	fs := flag.NewFlagSet("launch", flag.ContinueOnError)
	var cfg launcher.Config
	fs.StringVar(&cfg.ControllerURL, "controller", "", "controller URL the launcher and its Jobs reach")
	fs.StringVar(&cfg.LogsURL, "logs", "", "logs service URL handed to each Job")
	fs.StringVar(&cfg.CacheURL, "cache", "", "cache service URL handed to each Job")
	fs.StringVar(&cfg.GitcacheURL, "gitcache", "", "git cache URL handed to each Job")
	fs.StringVar(&cfg.DependencyProxyURL, "dependency-proxy", "", "cache URL whose go, npm and pip proxies each Job uses")
	fs.StringVar(&cfg.Namespace, "namespace", "sparkwing-jobs", "namespace the Jobs run in")
	fs.StringVar(&cfg.Image, "image", "", "runner image, pinned by digest")
	cpu := fs.String("cpu-ceiling", "", "most cores one Job may request (required)")
	memory := fs.String("memory-ceiling", "", "most memory one Job may request (required)")
	fs.DurationVar(&cfg.Deadline, "deadline", launcher.MaxDeadline, "a Job's life and its claim token's")
	fs.StringVar(&cfg.ScratchLimit, "scratch", "", "size limit of a Job's scratch volume (default 20Gi)")
	poll := fs.Duration("poll", time.Second, "how often an idle launcher asks for work")
	nodePool := fs.String("node-pool", "", "Karpenter NodePool whose CPU limit bounds the Jobs; empty reads capacity from the Jobs alone")
	token := fs.String("token", os.Getenv("SPARKWING_AGENT_TOKEN"),
		"the launcher's claims.launch bearer token (env: SPARKWING_AGENT_TOKEN)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	var err error
	if cfg.CPUCeiling, err = k8s.ParseCPUCeiling(*cpu); err != nil {
		return err
	}
	if cfg.MemoryCeiling, err = k8s.ParseMemoryCeiling(*memory); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("launch: a claims.launch token is required")
	}
	rc, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("kube config: %w", err)
	}
	kube, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return fmt.Errorf("kube client: %w", err)
	}
	holder, err := os.Hostname()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	l := &launcher.Launcher{
		Kube: kube, Ctrl: client.NewWithToken(cfg.ControllerURL, nil, *token), Config: cfg,
		Holder: "launcher:" + holder, Poll: *poll, Logger: slog.Default(),
	}
	if *nodePool != "" {
		dyn, err := dynamic.NewForConfig(rc)
		if err != nil {
			return fmt.Errorf("kube dynamic client: %w", err)
		}
		l.Capacity = launcher.NodePoolCapacity(dyn, *nodePool)
	}
	return l.Run(ctx)
}
