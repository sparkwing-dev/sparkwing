package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
	secretsPreviousKeyFile := fs.String("secrets-previous-key-file", "",
		"path to the key secret values were sealed under before the current one "+
			"(alternative to SPARKWING_SECRETS_PREVIOUS_KEY). Read-only: a stored "+
			"value that does not open under the current key is tried against this "+
			"one, so a key swap keeps every value readable until "+
			"`sparkwing secrets rotate` has re-encrypted them.")
	bootstrapAdminTokenFile := fs.String("bootstrap-admin-token-file", "",
		"path to a file holding the first admin token (alternative to "+
			"SPARKWING_BOOTSTRAP_ADMIN_TOKEN, which carries the value itself). "+
			"When the tokens table is empty the controller stores the hash of "+
			"that token as an admin credential before it binds the listener, so "+
			"a provisioned controller never serves a request unauthenticated. "+
			"Ignored once any token exists.")
	cachePodURL := fs.String("cache-pod-url", os.Getenv("CACHE_POD_URL"),
		"externally-reachable URL of the sparkwing-cache pod (gitcache + artifact store). "+
			"Announced via GET /api/v1/services so operator CLIs can discover it without "+
			"hardcoding it in profiles.yaml. Empty disables the announcement.")
	logsURL := fs.String("logs-url", os.Getenv("SPARKWING_LOGS_URL"),
		"externally-reachable URL of the sparkwing-logs service. Announced via "+
			"GET /api/v1/services so runners post node log lines to the service that "+
			"routes them; the controller itself serves no /api/v1/logs. Empty disables "+
			"the announcement, which is correct only when one process serves both.")
	dashboardURL := fs.String("dashboard-url", os.Getenv("SPARKWING_DASHBOARD_URL"),
		"externally-reachable URL of the dashboard that watches this controller. "+
			"Announced via GET /api/v1/services, so `sparkwing cloud connect` prints "+
			"where to watch runs. Empty disables the announcement.")
	cacheURL := fs.String("cache-url", os.Getenv("SPARKWING_CACHE_URL"),
		"controller-reachable sparkwing-cache URL for gitcache proxy routes")
	externalURL := fs.String("external-url", os.Getenv("SPARKWING_EXTERNAL_URL"),
		"base URL this controller answers on from outside the cluster, which is "+
			"where GitHub posts webhook deliveries. `sparkwing cluster webhooks "+
			"connect` points a repository's webhook at it. Empty answers each "+
			"connect request with the URL that request arrived at.")
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
	liveLogNodeKB := fs.Int("live-log-node-kb", controller.DefaultLiveLogNodeBytes>>10,
		"kilobytes of each running node's log the controller keeps in memory for "+
			"live readers. This is the live view for a deployment whose logs surface "+
			"is an object store; the durable copy is unaffected.")
	liveLogTotalMB := fs.Int("live-log-total-mb", int(controller.DefaultLiveLogTotalBytes>>20),
		"megabytes of live log buffers across every running node together. Past it "+
			"the controller drops the oldest bytes of the widest buffer.")
	liveLogMaxNodes := fs.Int("live-log-max-nodes", controller.DefaultLiveLogMaxNodes,
		"how many nodes may hold a live log buffer at once. Past it the controller "+
			"releases the buffer of the node that wrote least recently.")
	liveLogIdle := fs.Duration("live-log-idle", controller.DefaultLiveLogIdleTimeout,
		"how long a node that stopped writing without reporting that it finished "+
			"keeps its live log buffer.")
	defaultPreferLabels := fs.String("default-prefer-labels", os.Getenv("SPARKWING_DEFAULT_PREFER_LABELS"),
		"comma-separated label terms a node with no Prefers of its own prefers "+
			"its runner to advertise, in the Prefers term syntax. Empty leaves "+
			"such a node to the first claimant (env: SPARKWING_DEFAULT_PREFER_LABELS)")
	placementHold := fs.Duration("placement-hold", 20*time.Second,
		"how long a node is held back from a claim-mode runner that does not "+
			"advertise its preference, measured from the node's ready time, while "+
			"a runner that does advertise it is live and has a slot. Zero claims "+
			"first-in-first-out regardless of preference.")
	placementLiveness := fs.Duration("placement-liveness", 30*time.Second,
		"how recently a claim-mode runner must have polled for a claim to count "+
			"as live for the hold above")
	maxRunsPerPrincipalHour := fs.Int("max-runs-per-principal-hour", 0,
		"cap on the runs one principal may create in a rolling hour. A webhook "+
			"delivery counts against the repository it names. Past the cap the "+
			"controller answers 429 with a Retry-After and logs the principal and "+
			"the reason. The budget lives in controller memory, so a restart "+
			"refills every principal. Zero is unlimited.")
	shedQueueDepth := fs.Int("shed-queue-depth", 0,
		"pending-trigger depth past which a new webhook delivery or API "+
			"submission is shed with 503 and a Retry-After rather than queued. "+
			"Zero never sheds.")
	triggerDedupeWindow := fs.Duration("trigger-dedupe-window", 0,
		"how long a content-identical API submission answers with the run the "+
			"first one started instead of starting a second. GitHub deliveries "+
			"are deduped by delivery id and body digest regardless. Zero dedupes "+
			"no API submission.")
	claimsPerMinute := fs.Int("claims-per-runner-minute", 0,
		fmt.Sprintf("per-runner request budget on the claim routes, per rolling "+
			"minute, keyed on the token prefix together with the runner the "+
			"request names. Past it a claim is answered 429 with a Retry-After "+
			"naming the refill delay. Zero is unlimited; %d suits the cadence the "+
			"shipped runners poll at.", controller.RecommendedClaimsPerMinute))
	heartbeatsPerMinute := fs.Int("heartbeats-per-runner-minute", 0,
		fmt.Sprintf("per-runner request budget on the heartbeat routes, per "+
			"rolling minute. The agent liveness heartbeat is never budgeted. "+
			"Zero is unlimited; %d suits the cadence the shipped runners "+
			"heartbeat at.", controller.RecommendedHeartbeatsPerMinute))
	idleClaimPoll := fs.Duration("idle-claim-poll", controller.DefaultMaxIdleClaimPoll,
		"widest poll interval this controller suggests to a claim loop while it "+
			"has no work to hand out. The suggestion travels as a response header "+
			"and a runner honors it only to poll less often, so an agent that "+
			"ignores it keeps its configured cadence. Zero suggests nothing.")
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
	if *liveLogNodeKB < 1 {
		return fmt.Errorf("--live-log-node-kb must be at least 1")
	}
	if *liveLogTotalMB < 1 {
		return fmt.Errorf("--live-log-total-mb must be at least 1")
	}
	if *liveLogMaxNodes < 1 {
		return fmt.Errorf("--live-log-max-nodes must be at least 1")
	}
	if *liveLogIdle <= 0 {
		return fmt.Errorf("--live-log-idle must be positive")
	}
	if *maxRunsPerPrincipalHour < 0 {
		return fmt.Errorf("--max-runs-per-principal-hour cannot be negative")
	}
	if *shedQueueDepth < 0 {
		return fmt.Errorf("--shed-queue-depth cannot be negative")
	}
	if *triggerDedupeWindow < 0 {
		return fmt.Errorf("--trigger-dedupe-window cannot be negative")
	}
	if *claimsPerMinute < 0 || *heartbeatsPerMinute < 0 {
		return fmt.Errorf("--claims-per-runner-minute and --heartbeats-per-runner-minute cannot be negative")
	}
	if *idleClaimPoll < 0 {
		return fmt.Errorf("--idle-claim-poll cannot be negative")
	}
	if err := checkIdleClaimPoll(*idleClaimPoll, *placementHold, *placementLiveness); err != nil {
		return err
	}
	if int64(*liveLogNodeKB)<<10 > int64(*liveLogTotalMB)<<20 {
		return fmt.Errorf("--live-log-node-kb (%d) exceeds --live-log-total-mb (%d), so one node would never fit",
			*liveLogNodeKB, *liveLogTotalMB)
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

	bootstrapToken, bterr := loadBootstrapAdminToken(*bootstrapAdminTokenFile)
	if bterr != nil {
		return fmt.Errorf("load bootstrap admin token: %w", bterr)
	}
	created, bterr := controller.EnsureBootstrapAdminToken(st, bootstrapToken, time.Now().UTC())
	if bterr != nil {
		return fmt.Errorf("bootstrap admin token: %w", bterr)
	}
	if bootstrapToken != "" {
		if created {
			fmt.Fprintln(os.Stderr,
				"sparkwing-controller: bootstrap admin token created for principal "+
					controller.BootstrapAdminPrincipal)
		} else {
			fmt.Fprintln(os.Stderr,
				"sparkwing-controller: bootstrap admin token skipped: the tokens table already holds a token")
		}
	}

	cipher, cerr := loadSecretsCipher(*secretsKeyFile, *secretsPreviousKeyFile)
	if cerr != nil {
		return fmt.Errorf("load secrets key: %w", cerr)
	}
	if cipher == nil {
		fmt.Fprintln(os.Stderr,
			"sparkwing-controller: WARNING: no secrets key configured "+
				"(SPARKWING_SECRETS_KEY / --secrets-key-file unset); "+
				"secret values will be stored at rest as plaintext")
	}

	webhookCfg, whErr := controller.ParseGitHubWebhookConfig(os.Getenv("GITHUB_WEBHOOK_BINDINGS"))
	if whErr != nil {
		return fmt.Errorf("GITHUB_WEBHOOK_BINDINGS: %w", whErr)
	}
	wh := webhookCfg.BindingCounts()
	fmt.Fprintf(os.Stderr,
		"sparkwing-controller: github webhook bindings: %d pipelines, %d bound repositories, "+
			"%d pipelines refusing every repository, %d repository secrets\n",
		wh.Pipelines, wh.Repos, wh.DenyAll, wh.RepoSecrets)

	srv := controller.New(st, nil).
		WithTrustedProxyCIDRs(trustedProxyCIDRs).
		EnableAuthFromStore().
		WithGitHubWebhookSecret(os.Getenv("GITHUB_WEBHOOK_SECRET")).
		WithGitHubWebhookConfig(webhookCfg).
		WithGitHubCommitStatuses(os.Getenv("GITHUB_TOKEN"), *dashboardURL).
		WithCachePodURL(*cachePodURL).
		WithLogsURL(*logsURL).
		WithDashboardURL(*dashboardURL).
		WithCacheURL(*cacheURL).
		WithExternalURL(*externalURL).
		WithMetricsAddr(*metricsAddr).
		WithLiveLogLimits(*liveLogNodeKB<<10, int64(*liveLogTotalMB)<<20, *liveLogMaxNodes, *liveLogIdle).
		WithLocalFirstPlacement(splitCSV(*defaultPreferLabels), *placementHold, *placementLiveness).
		WithFloodPolicy(controller.FloodPolicy{
			RunsPerPrincipalHour: *maxRunsPerPrincipalHour,
			ShedQueueDepth:       *shedQueueDepth,
			DedupeWindow:         *triggerDedupeWindow,
		}).
		WithRequestBudget(controller.RequestBudget{
			ClaimsPerMinute:     *claimsPerMinute,
			HeartbeatsPerMinute: *heartbeatsPerMinute,
		}).
		WithIdleClaimPoll(*idleClaimPoll)
	// safety: a typed-nil *secrets.Cipher satisfies the interface and would register as non-nil at the handler's seam.
	if cipher != nil {
		srv = srv.WithSecretsCipher(cipher)
	}
	if *requireAuth && !srv.AuthEnabled() {
		return fmt.Errorf("--require-auth (SPARKWING_REQUIRE_AUTH) is set but " +
			"the tokens table is empty; supply the first admin token with " +
			"--bootstrap-admin-token-file (SPARKWING_BOOTSTRAP_ADMIN_TOKEN), or " +
			"mint one with the controller started unauthenticated and restart " +
			"with --require-auth")
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

// safety: a runner honoring a suggestion longer than these windows stops
// counting as live, and local-first placement silently stops preferring it.
func checkIdleClaimPoll(idle, hold, liveness time.Duration) error {
	longest := controller.LongestHonoredIdlePoll(idle)
	for _, w := range []struct {
		flag  string
		value time.Duration
	}{
		{"--placement-hold", hold},
		{"--placement-liveness", liveness},
	} {
		if w.value > 0 && longest >= w.value {
			return fmt.Errorf(
				"--idle-claim-poll %s stretches to %s once a runner spreads it, which is not below %s %s; "+
					"lower the suggestion or raise that window",
				idle, longest, w.flag, w.value)
		}
	}
	return nil
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

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func loadSecretsCipher(keyFile, previousKeyFile string) (*secrets.Cipher, error) {
	key, err := loadSecretsKey("SPARKWING_SECRETS_KEY", keyFile)
	if err != nil {
		return nil, err
	}
	previous, err := loadSecretsKey("SPARKWING_SECRETS_PREVIOUS_KEY", previousKeyFile)
	if err != nil {
		return nil, err
	}
	if key == nil {
		if previous != nil {
			return nil, errors.New("a previous secrets key is configured without a current one; " +
				"set SPARKWING_SECRETS_KEY or --secrets-key-file to the key values are sealed under now")
		}
		return nil, nil
	}
	if previous == nil {
		return secrets.NewCipher(key)
	}
	return secrets.NewCipherWithPrevious(key, previous)
}

func loadSecretsKey(envName, filePath string) ([]byte, error) {
	v := os.Getenv(envName)
	clearEnv(envName)
	if v != "" {
		key, err := secrets.DecodeKey(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", envName, err)
		}
		return key, nil
	}
	if filePath == "" {
		return nil, nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}
	if len(data) == secrets.KeySize {
		return data, nil
	}
	decoded, derr := secrets.DecodeKey(string(data))
	if derr != nil {
		return nil, fmt.Errorf("%s: %w", filePath, derr)
	}
	return decoded, nil
}

// safety: a trailing newline from a mounted file or a heredoc is editor noise, not part of the credential.
func loadBootstrapAdminToken(filePath string) (string, error) {
	fromEnv := os.Getenv("SPARKWING_BOOTSTRAP_ADMIN_TOKEN")
	clearEnv("SPARKWING_BOOTSTRAP_ADMIN_TOKEN")
	if v := strings.TrimSpace(fromEnv); v != "" {
		return v, nil
	}
	if filePath == "" {
		return "", nil
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", filePath, err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("%s is empty", filePath)
	}
	return token, nil
}

// safety: a credential left in the environment reaches every child process and anything that reads /proc.
func clearEnv(name string) {
	if err := os.Unsetenv(name); err != nil {
		fmt.Fprintf(os.Stderr, "sparkwing-controller: could not clear %s from the environment: %v\n", name, err)
	}
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
