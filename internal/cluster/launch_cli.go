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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/logutil"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
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
	readLog := logutil.Bind(fs)
	credentialsDir := fs.String(credentials.FlagName, "",
		"directory holding "+agentTokenCredential+", the launcher's claims.launch bearer token (required)")
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
	creds, err := credentials.Open(*credentialsDir)
	if err != nil {
		return err
	}
	token, err := creds.Read(agentTokenCredential)
	if err != nil {
		return err
	}
	if token == "" {
		return errors.New("launch: a claims.launch token is required in " + agentTokenCredential + " under --" + credentials.FlagName)
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
	tel, err := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-launcher", Log: readLog()})
	if err != nil {
		return err
	}
	defer func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			slog.Warn("telemetry shutdown", "err", err)
		}
	}()
	l := &launcher.Launcher{
		Kube: kube, Ctrl: client.NewWithToken(cfg.ControllerURL, nil, token), Config: cfg,
		Holder: "launcher:" + holder, Poll: *poll, Logger: slog.Default(),
	}
	return l.Run(ctx)
}
