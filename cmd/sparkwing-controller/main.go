package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/pool"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-controller:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sparkwing-controller", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:4344", "bind address")
	metricsAddr := fs.String("metrics-addr", os.Getenv("SPARKWING_METRICS_ADDR"),
		"bind address for the Prometheus /metrics endpoint. Set it to move "+
			"/metrics off the API listener, and off any ingress fronting that "+
			"listener, onto its own port. Empty serves /metrics on --addr.")
	poolEnabled := fs.Bool("pool", false,
		"enable the warm-PVC pool (requires in-cluster K8s access)")
	poolNamespace := fs.String("pool-namespace", os.Getenv("POD_NAMESPACE"),
		"namespace the pool manages (default: POD_NAMESPACE)")
	warmerServiceAccount := fs.String("warmer-service-account",
		firstNonEmpty(os.Getenv("SPARKWING_WARMER_SA"), pool.WarmerServiceAccountName),
		"ServiceAccount the warm-pool warmer pods run as; it must exist in the pool "+
			"namespace and needs no rules (env: SPARKWING_WARMER_SA)")
	kubeconfig := fs.String("kubeconfig", os.Getenv("KUBECONFIG"),
		"kubeconfig path when --pool is set (empty = in-cluster)")
	secretsKeyFile := fs.String("secrets-key-file", "",
		"path to a file containing 32 raw bytes for secret encryption (alternative to SPARKWING_SECRETS_KEY)")
	cachePodURL := fs.String("cache-pod-url", os.Getenv("CACHE_POD_URL"),
		"externally-reachable URL of the sparkwing-cache pod (gitcache + artifact store). "+
			"Announced via GET /api/v1/services so operator CLIs can discover it without "+
			"hardcoding it in profiles.yaml. Empty disables the announcement.")
	logsURL := fs.String("logs-url", os.Getenv("SPARKWING_LOGS_URL"),
		"externally-reachable URL of the sparkwing-logs service. Announced via "+
			"GET /api/v1/services so runners post node log lines to the service that "+
			"routes them; the controller itself serves no /api/v1/logs. Empty disables "+
			"the announcement, which is correct only when one process serves both.")
	cacheURL := fs.String("cache-url", os.Getenv("SPARKWING_CACHE_URL"),
		"controller-reachable sparkwing-cache URL for gitcache proxy routes")
	trustedProxyCIDRsRaw := fs.String("trusted-proxy-cidrs", "",
		"comma-separated proxy source CIDRs allowed to supply X-Forwarded-For "+
			"for login throttling; empty ignores forwarded headers and keys the "+
			"limiter on the TCP peer")
	argonBudgetMB := fs.Int("argon2-memory-budget-mb",
		int(store.DefaultArgon2MemoryBudget>>20),
		fmt.Sprintf("memory ceiling in MiB for concurrent argon2id password and "+
			"token hashing. Each hash holds %d MiB while it runs, so the default "+
			"admits %d at a time and the rest queue.",
			store.Argon2HashBytes>>20,
			store.Argon2Concurrency(store.DefaultArgon2MemoryBudget)))
	requireAuth := fs.Bool("require-auth", envTruthy("SPARKWING_REQUIRE_AUTH"),
		"refuse to start when the tokens table is empty, guarding against "+
			"accidentally deploying an open controller. Leave unset for "+
			"first-run bootstrap (minting the first token needs an open "+
			"controller) and for laptop-local use.")
	_ = fs.Parse(args)

	trustedProxyCIDRs, err := ratelimit.ParseTrustedProxyCIDRs(*trustedProxyCIDRsRaw)
	if err != nil {
		return fmt.Errorf("--trusted-proxy-cidrs: %w", err)
	}
	if *argonBudgetMB < 1 {
		return fmt.Errorf("--argon2-memory-budget-mb must be at least 1")
	}
	store.SetArgon2MemoryBudget(int64(*argonBudgetMB) << 20)

	emitStartupProvenance(os.Stderr)

	p, perr := paths.DefaultPaths()
	if perr != nil {
		return perr
	}
	if err := p.EnsureRoot(); err != nil {
		return err
	}
	st, serr := store.Open(p.StateDB())
	if serr != nil {
		return mapStoreOpenError(serr)
	}
	defer func() { _ = st.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-controller"})
	defer func() { _ = tel.Shutdown(context.Background()) }()

	cipher, cerr := loadSecretsCipher(*secretsKeyFile)
	if cerr != nil {
		return fmt.Errorf("load secrets key: %w", cerr)
	}
	if cipher == nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: WARNING: no secrets key configured "+
				"(SPARKWING_SECRETS_KEY / --secrets-key-file unset); "+
				"secret values will be stored at rest as plaintext")
	}

	webhookCfg, whErr := parseGitHubWebhookConfig(os.Getenv("GITHUB_WEBHOOK_BINDINGS"))
	if whErr != nil {
		return fmt.Errorf("GITHUB_WEBHOOK_BINDINGS: %w", whErr)
	}

	srv := controller.New(st, nil).
		WithTrustedProxyCIDRs(trustedProxyCIDRs).
		EnableAuthFromStore().
		WithGitHubWebhookSecret(os.Getenv("GITHUB_WEBHOOK_SECRET")).
		WithGitHubWebhookConfig(webhookCfg).
		WithGitHubCommitStatuses(os.Getenv("GITHUB_TOKEN"), os.Getenv("SPARKWING_DASHBOARD_URL")).
		WithCachePodURL(*cachePodURL).
		WithLogsURL(*logsURL).
		WithCacheURL(*cacheURL).
		WithMetricsAddr(*metricsAddr)
	// safety: a typed-nil *secrets.Cipher satisfies the interface and would register as non-nil at the handler's seam.
	if cipher != nil {
		srv = srv.WithSecretsCipher(cipher)
	}
	if *requireAuth && !srv.AuthEnabled() {
		return fmt.Errorf("--require-auth (SPARKWING_REQUIRE_AUTH) is set but " +
			"the tokens table is empty; mint an admin token with the controller " +
			"started unauthenticated, then restart with --require-auth")
	}
	if *poolEnabled {
		if *poolNamespace == "" {
			return fmt.Errorf("--pool requires --pool-namespace (or POD_NAMESPACE)")
		}
		kcli, kerr := kubeClient(*kubeconfig)
		if kerr != nil {
			return fmt.Errorf("pool: %w", kerr)
		}
		srv.AttachPool(controller.PoolConfig{
			Client:               kcli,
			Namespace:            *poolNamespace,
			WarmerServiceAccount: *warmerServiceAccount,
		})
		checkStorageClasses(ctx, kcli, *poolNamespace)
	}
	return controller.ServeWith(ctx, srv, *addr)
}

func checkStorageClasses(ctx context.Context, kcli kubernetes.Interface, namespace string) {
	pvcs, err := kcli.CoreV1().PersistentVolumeClaims(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: storage check skipped: list PVCs:", err)
		return
	}
	for _, pvc := range pvcs.Items {
		if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
			return
		}
	}
	classes, err := kcli.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: storage check skipped: list StorageClasses:", err)
		return
	}
	const defaultAnnotation = "storageclass.kubernetes.io/is-default-class"
	for _, sc := range classes.Items {
		if sc.Annotations[defaultAnnotation] == "true" {
			return
		}
	}
	fmt.Fprintln(os.Stderr,
		"sparkwing-controller: WARNING: no PVC declares storageClassName "+
			"and the cluster has no default StorageClass; PVCs will hang "+
			"Pending. Set storageClassName on the PVCs (helm: "+
			"--set storage.className=<class>) or mark a StorageClass "+
			"default with storageclass.kubernetes.io/is-default-class=true.")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func loadSecretsCipher(filePath string) (*secrets.Cipher, error) {
	if v := os.Getenv("SPARKWING_SECRETS_KEY"); v != "" {
		key, err := secrets.DecodeKey(v)
		if err != nil {
			return nil, fmt.Errorf("SPARKWING_SECRETS_KEY: %w", err)
		}
		return secrets.NewCipher(key)
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filePath, err)
		}
		if len(data) == secrets.KeySize {
			return secrets.NewCipher(data)
		}
		decoded, derr := secrets.DecodeKey(string(data))
		if derr != nil {
			return nil, fmt.Errorf("%s: %w", filePath, derr)
		}
		return secrets.NewCipher(decoded)
	}
	return nil, nil
}

func kubeClient(kubeconfig string) (kubernetes.Interface, error) {
	var rc *rest.Config
	var err error
	if kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("kube config: %w", err)
	}
	return kubernetes.NewForConfig(rc)
}

func parseGitHubWebhookConfig(raw string) (controller.GitHubWebhookConfig, error) {
	var cfg controller.GitHubWebhookConfig
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return controller.GitHubWebhookConfig{}, err
	}
	return cfg, nil
}
