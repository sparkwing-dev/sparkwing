package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/bincache"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/pool"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
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
	runnersPerToken := fs.Int("runners-per-token", controller.DefaultRunnersPerToken,
		"most self-named runners one caller may hold at once on the claim routes, "+
			"where the runner name is the caller's own word and each name gets its "+
			"own request budget. A name counts until it goes unused for ten "+
			"minutes, and a new name past the cap is answered 429.")
	claimsPerMinute := fs.Int(flagClaimsPerRunnerMinute, 0,
		fmt.Sprintf("per-runner request budget on the claim routes, per rolling "+
			"minute, keyed on the token prefix together with the runner the "+
			"request names. Past it a claim is answered 429 with a Retry-After "+
			"naming the refill delay. Zero is unlimited. A claim that comes back "+
			"with a node spends nothing: an award is work this controller handed "+
			"out and the loop re-claims at once, so the budget bounds empty "+
			"polling, which a runner can do without limit. Work it from that "+
			"cadence rather than a round number: the loop polls once every %s "+
			"while the queue is empty, which is %d requests a minute, so %d "+
			"allows four times that and %d eight.",
			controller.ClaimPollInterval,
			controller.CompliantClaimPollsPerMinute(),
			controller.CompliantClaimPollsPerMinute()*4,
			controller.CompliantClaimPollsPerMinute()*8))
	heartbeatsPerMinute := fs.Int(flagHeartbeatsPerRunnerMinute, 0,
		fmt.Sprintf("per-runner request budget on the heartbeat routes, per "+
			"rolling minute, charged whatever the answer. The agent liveness "+
			"heartbeat is never budgeted. Zero is unlimited; %d suits the "+
			"cadence the shipped runners heartbeat at.",
			controller.RecommendedHeartbeatsPerMinute))
	requestsPerTokenMinute := fs.Int(flagRequestsPerTokenMinute, 0,
		"request budget on every route one token can reach, per rolling minute, "+
			"keyed on the token prefix. It bounds a caller that varies the runner "+
			"it says it is, which the per-runner budgets above cannot. A liveness "+
			"heartbeat for the agent this token enrolled is never the request "+
			"that is shed; one naming any other agent is budgeted like the rest. "+
			"Past the budget a request is answered 429 with a Retry-After naming "+
			"the refill delay. Zero is unlimited.")
	requestsPerMinuteAlarm := fs.Int(flagRequestsPerMinuteAlarm, 0,
		"request rate, across every caller, past which this controller logs at "+
			"warn and counts sparkwing_request_rate_alarm_total. It refuses "+
			"nothing; it is the notice that one pod is serving more than it was "+
			"sized for. Zero raises no alarm.")
	limitsProfile := fs.String("limits-profile", "",
		fmt.Sprintf("named set of abuse guards this controller runs with: %s. A profile "+
			"supplies the per-runner and per-token request budgets, the request rate "+
			"alarm, the egress stream and download caps, and idle-poll enforcement, "+
			"and it fills a guard only where the command line and the environment "+
			"named none, so an explicit setting always wins, zero included. Empty, "+
			"the default, supplies none and leaves a self-hosted controller exactly "+
			"as it was.",
			strings.Join(controller.LimitsProfileNames(), ", ")))
	idleClaimPoll := fs.Duration("idle-claim-poll", controller.DefaultMaxIdleClaimPoll,
		"widest poll interval this controller suggests to a claim loop while it "+
			"has no work to hand out. The suggestion travels as a response header "+
			"and a runner honors it only to poll less often, so an agent that "+
			"ignores it keeps its configured cadence. Zero suggests nothing.")
	ceilingDefaults, cerr := objectguard.ConfigFromEnv(os.Getenv)
	if cerr != nil {
		return cerr
	}
	maxBucketBytes := fs.Int64("max-bucket-bytes", ceilingDefaults.Ceiling.Limit.MaxBytes,
		"stored bytes across the whole object store at or above which the controller freezes "+
			"object writes: existing runs finish, new writes are refused naming the ceiling, "+
			"and health reports the freeze. 0, the default, leaves the bucket unlimited "+
			"(env: SPARKWING_OBJECT_STORE_MAX_BUCKET_BYTES)")
	maxBucketObjects := fs.Int64("max-bucket-objects", ceilingDefaults.Ceiling.Limit.MaxObjects,
		"objects across the whole object store at or above which the controller freezes object "+
			"writes; 0 leaves the count unlimited (env: SPARKWING_OBJECT_STORE_MAX_BUCKET_OBJECTS)")
	warnBucketBytes := fs.Int64("warn-bucket-bytes", ceilingDefaults.Ceiling.Limit.WarnBytes,
		"stored bytes at which health reports the bucket as warning, which refuses nothing; "+
			"0 disables the warning (env: SPARKWING_OBJECT_STORE_WARN_BUCKET_BYTES)")
	warnBucketObjects := fs.Int64("warn-bucket-objects", ceilingDefaults.Ceiling.Limit.WarnObjects,
		"objects at which health reports the bucket as warning; 0 disables the warning "+
			"(env: SPARKWING_OBJECT_STORE_WARN_BUCKET_OBJECTS)")
	bucketStoreURL := fs.String("bucket-store", os.Getenv("SPARKWING_OBJECT_STORE_URL"),
		"object store the bucket ceiling measures, as a store URL such as "+
			"s3://bucket/prefix. It is read on the reconciliation interval and never "+
			"served, so the controller exposes none of it. Empty counts only the writes "+
			"this process makes (env: SPARKWING_OBJECT_STORE_URL)")
	bucketMeasurePages := fs.Int("bucket-measure-pages", envMeasurePages(),
		"listings one bucket measurement may spend before it stops and reports itself "+
			"incomplete. Each listing covers a thousand objects, so the default bounds a "+
			"measurement at a million; raise it for a larger bucket, or leave the ceiling "+
			"on its running count (env: SPARKWING_OBJECT_STORE_BUCKET_MEASURE_PAGES)")
	bucketReconcile := fs.Duration("bucket-reconcile", ceilingDefaults.Ceiling.Reconcile,
		"how often the controller measures the whole bucket and replaces the running "+
			"count with the measurement. Writes are counted as they happen, so this "+
			"listing is the only enumeration the ceiling costs; 0 measures once at "+
			"startup and never again (env: SPARKWING_OBJECT_STORE_BUCKET_RECONCILE)")
	readEgress := egress.Bind(fs, os.Getenv, egress.ServiceController, egress.ControllerSurfaces)
	licenseFile := fs.String("license-file", "",
		"file holding the signed license that unlocks multi-team hosting. "+
			"Empty reads the license text from SPARKWING_LICENSE; without a "+
			"valid license the controller holds one team.")
	googleClientID := fs.String("google-client-id", os.Getenv("SPARKWING_GOOGLE_CLIENT_ID"),
		"Google OAuth client id for dashboard sign-in; the secret comes from "+
			"SPARKWING_GOOGLE_CLIENT_SECRET. Offered only with a multi-team license.")
	githubClientID := fs.String("github-client-id", os.Getenv("SPARKWING_GITHUB_CLIENT_ID"),
		"GitHub OAuth app client id for dashboard sign-in; the secret comes from "+
			"SPARKWING_GITHUB_CLIENT_SECRET. Offered only with a multi-team license.")
	oauthRedirectURIs := fs.String("oauth-redirect-uris", os.Getenv("SPARKWING_OAUTH_REDIRECT_URIS"),
		"comma-separated dashboard callback URLs a sign-in may return to, "+
			"such as https://app.example.com/auth/google/callback")
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
	egressCfg, egressNamed, err := readEgress()
	if err != nil {
		return err
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
	if *runnersPerToken < 1 {
		return fmt.Errorf("--runners-per-token must be at least 1")
	}
	if *claimsPerMinute < 0 || *heartbeatsPerMinute < 0 {
		return fmt.Errorf("--claims-per-runner-minute and --heartbeats-per-runner-minute cannot be negative")
	}
	if *requestsPerTokenMinute < 0 || *requestsPerMinuteAlarm < 0 {
		return fmt.Errorf("--requests-per-token-minute and --requests-per-minute-alarm cannot be negative")
	}
	if *idleClaimPoll < 0 {
		return fmt.Errorf("--idle-claim-poll cannot be negative")
	}
	if err := checkIdleClaimPoll(*idleClaimPoll, *placementHold, *placementLiveness); err != nil {
		return err
	}
	profile, err := controller.LimitsProfile(*limitsProfile)
	if err != nil {
		return fmt.Errorf("--limits-profile: %w", err)
	}
	guards := applyLimitsProfile(profile, guardValues{
		ClaimsPerRunnerMinute:     *claimsPerMinute,
		HeartbeatsPerRunnerMinute: *heartbeatsPerMinute,
		RequestsPerTokenMinute:    *requestsPerTokenMinute,
		RequestsPerMinuteAlarm:    *requestsPerMinuteAlarm,
		MaxLogStreamsPerPrincipal: egressCfg.MaxStreamsPerPrincipal,
		MaxDownloadsPerPrincipal:  egressCfg.MaxDownloadsPerPrincipal,
		RunsPerPrincipalHour:      *maxRunsPerPrincipalHour,
		ShedQueueDepth:            *shedQueueDepth,
		EgressMonthlyBytes:        egressCfg.PerPrincipalMonthlyBytes,
		EgressDailyCapBytes:       egressCfg.GlobalDailyCapBytes,
	}, guardsNamed{
		ClaimsPerRunnerMinute:     fs.Changed(flagClaimsPerRunnerMinute),
		HeartbeatsPerRunnerMinute: fs.Changed(flagHeartbeatsPerRunnerMinute),
		RequestsPerTokenMinute:    fs.Changed(flagRequestsPerTokenMinute),
		RequestsPerMinuteAlarm:    fs.Changed(flagRequestsPerMinuteAlarm),
		MaxLogStreamsPerPrincipal: egressNamed.MaxLogStreams,
		MaxDownloadsPerPrincipal:  egressNamed.MaxDownloads,
		RunsPerPrincipalHour:      fs.Changed("max-runs-per-principal-hour"),
		ShedQueueDepth:            fs.Changed("shed-queue-depth"),
		EgressMonthlyBytes:        egressNamed.MonthlyBytes,
		EgressDailyCapBytes:       egressNamed.DailyCapBytes,
	})
	egressCfg.MaxStreamsPerPrincipal = guards.MaxLogStreamsPerPrincipal
	egressCfg.MaxDownloadsPerPrincipal = guards.MaxDownloadsPerPrincipal
	egressCfg.PerPrincipalMonthlyBytes = guards.EgressMonthlyBytes
	egressCfg.GlobalDailyCapBytes = guards.EgressDailyCapBytes
	if int64(*liveLogNodeKB)<<10 > int64(*liveLogTotalMB)<<20 {
		return fmt.Errorf("--live-log-node-kb (%d) exceeds --live-log-total-mb (%d), so one node would never fit",
			*liveLogNodeKB, *liveLogTotalMB)
	}
	store.SetArgon2MemoryBudget(int64(*argonBudgetMB) << 20)
	if err := applyBucketCeiling(objectguard.CeilingConfig{
		Limit: objectguard.CeilingLimit{
			MaxBytes:    *maxBucketBytes,
			MaxObjects:  *maxBucketObjects,
			WarnBytes:   *warnBucketBytes,
			WarnObjects: *warnBucketObjects,
		},
		Reconcile: *bucketReconcile,
	}); err != nil {
		return err
	}

	emitStartupProvenance(os.Stderr)

	p, perr := paths.DefaultPaths()
	if perr != nil {
		return perr
	}
	if err := p.EnsureRoot(); err != nil {
		return err
	}
	st, serr := openControllerStore(context.Background(), p.StateDB())
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
			RunsPerPrincipalHour: guards.RunsPerPrincipalHour,
			ShedQueueDepth:       guards.ShedQueueDepth,
			DedupeWindow:         *triggerDedupeWindow,
		}).
		WithRequestBudget(controller.RequestBudget{
			ClaimsPerMinute:     guards.ClaimsPerRunnerMinute,
			HeartbeatsPerMinute: guards.HeartbeatsPerRunnerMinute,
			RunnersPerToken:     *runnersPerToken,
		}).
		WithTokenRequestBudget(controller.TokenRequestBudget{
			PerTokenMinute: guards.RequestsPerTokenMinute,
			AlarmPerMinute: guards.RequestsPerMinuteAlarm,
		}).
		WithIdleClaimPoll(*idleClaimPoll).
		WithIdleClaimPollEnforced(guards.EnforceIdleClaimPoll).
		WithEgressMeter(egress.New(egressCfg))
	if err := configureIdentity(srv, identityFlags{
		LicenseFile:        *licenseFile,
		GoogleClientID:     *googleClientID,
		GoogleClientSecret: os.Getenv("SPARKWING_GOOGLE_CLIENT_SECRET"),
		GitHubClientID:     *githubClientID,
		GitHubClientSecret: os.Getenv("SPARKWING_GITHUB_CLIENT_SECRET"),
		RedirectURIs:       *oauthRedirectURIs,
	}, slog.Default()); err != nil {
		return err
	}
	if err := checkCacheGrantKey(srv, *cacheURL, *cachePodURL,
		os.Getenv(authwire.CacheGrantKeyEnv), bincache.CacheToken()); err != nil {
		return err
	}
	// The license decides whether an empty tokens table may serve
	// unauthenticated, so auth is resolved after it is installed.
	srv.EnableAuthFromStore()
	if err := configureSecrets(ctx, srv, cipher); err != nil {
		return err
	}
	if *bucketMeasurePages < 1 {
		return fmt.Errorf("--bucket-measure-pages must be at least 1; a measurement that lists nothing can only be incomplete")
	}
	if *bucketStoreURL != "" {
		bucketStore, berr := storeurl.OpenMeasurementStore(ctx, *bucketStoreURL, *bucketMeasurePages)
		if berr != nil {
			return fmt.Errorf("--bucket-store: %w", berr)
		}
		srv = srv.WithBucketUsage(bucketStore)
	}
	if err := checkRequireAuth(st, *requireAuth); err != nil {
		return err
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
// The margin is a whole second poll, because enforcement never names a wait
// longer than one suggestion and a refused runner must still land inside the
// window on its second try.
const idleClaimPollMargin = 2

// safety: one name per flag, because the profile has to ask the flag set
// whether the operator named a guard and a second spelling would answer for a
// flag nobody set.
const (
	flagClaimsPerRunnerMinute     = "claims-per-runner-minute"
	flagHeartbeatsPerRunnerMinute = "heartbeats-per-runner-minute"
	flagRequestsPerTokenMinute    = "requests-per-token-minute"
	flagRequestsPerMinuteAlarm    = "requests-per-minute-alarm"
)

type guardValues struct {
	ClaimsPerRunnerMinute     int
	HeartbeatsPerRunnerMinute int
	RequestsPerTokenMinute    int
	RequestsPerMinuteAlarm    int
	MaxLogStreamsPerPrincipal int
	MaxDownloadsPerPrincipal  int
	RunsPerPrincipalHour      int
	ShedQueueDepth            int
	EgressMonthlyBytes        int64
	EgressDailyCapBytes       int64
	EnforceIdleClaimPoll      bool
}

// safety: a guard the operator named wins whatever its value, so the profile
// has to be told which ones were named rather than reading zero as unset.
type guardsNamed struct {
	ClaimsPerRunnerMinute     bool
	HeartbeatsPerRunnerMinute bool
	RequestsPerTokenMinute    bool
	RequestsPerMinuteAlarm    bool
	MaxLogStreamsPerPrincipal bool
	MaxDownloadsPerPrincipal  bool
	RunsPerPrincipalHour      bool
	ShedQueueDepth            bool
	EgressMonthlyBytes        bool
	EgressDailyCapBytes       bool
}

// safety: zero is a documented value on every guard here, unlimited, so what
// the operator left alone is what the flag set reports rather than what the
// value happens to be; an explicit zero keeps its guard off under a profile.
func applyLimitsProfile(profile controller.LimitsProfileValues, set guardValues, named guardsNamed) guardValues {
	if !named.ClaimsPerRunnerMinute {
		set.ClaimsPerRunnerMinute = profile.ClaimsPerRunnerMinute
	}
	if !named.HeartbeatsPerRunnerMinute {
		set.HeartbeatsPerRunnerMinute = profile.HeartbeatsPerRunnerMinute
	}
	if !named.RequestsPerTokenMinute {
		set.RequestsPerTokenMinute = profile.RequestsPerTokenMinute
	}
	if !named.RequestsPerMinuteAlarm {
		set.RequestsPerMinuteAlarm = profile.RequestsPerMinuteAlarm
	}
	if !named.MaxLogStreamsPerPrincipal {
		set.MaxLogStreamsPerPrincipal = profile.MaxLogStreamsPerPrincipal
	}
	if !named.MaxDownloadsPerPrincipal {
		set.MaxDownloadsPerPrincipal = profile.MaxDownloadsPerPrincipal
	}
	if !named.RunsPerPrincipalHour {
		set.RunsPerPrincipalHour = profile.RunsPerPrincipalHour
	}
	if !named.ShedQueueDepth {
		set.ShedQueueDepth = profile.ShedQueueDepth
	}
	if !named.EgressMonthlyBytes {
		set.EgressMonthlyBytes = profile.EgressMonthlyBytesPerPrincipal
	}
	if !named.EgressDailyCapBytes {
		set.EgressDailyCapBytes = profile.EgressDailyCapBytes
	}
	set.EnforceIdleClaimPoll = profile.EnforceIdleClaimPoll
	return set
}

func checkIdleClaimPoll(idle, hold, liveness time.Duration) error {
	longest := controller.LongestHonoredIdlePoll(idle)
	for _, w := range []struct {
		flag  string
		value time.Duration
	}{
		{"--placement-hold", hold},
		{"--placement-liveness", liveness},
	} {
		if w.value > 0 && longest*idleClaimPollMargin > w.value {
			return fmt.Errorf(
				"--idle-claim-poll %s stretches to %s once a runner spreads it, and %d of those do not fit in %s %s; "+
					"lower the suggestion or raise that window",
				idle, longest, idleClaimPollMargin, w.flag, w.value)
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

// checkRequireAuth refuses a --require-auth start with no live token. It asks
// the tokens table rather than whether auth is on, because a multi-team
// license turns auth on with an empty table and --require-auth promises a
// token an operator can use.
func checkRequireAuth(st *store.Store, requireAuth bool) error {
	if !requireAuth {
		return nil
	}
	toks, err := st.ListTokens("", false)
	if err != nil {
		return fmt.Errorf("--require-auth: read the tokens table: %w", err)
	}
	if len(toks) > 0 {
		return nil
	}
	return fmt.Errorf("--require-auth (SPARKWING_REQUIRE_AUTH) is set but " +
		"the tokens table is empty; supply the first admin token with " +
		"--bootstrap-admin-token-file (SPARKWING_BOOTSTRAP_ADMIN_TOKEN), or " +
		"mint one with the controller started unauthenticated and restart " +
		"with --require-auth")
}

func envTruthy(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// checkCacheGrantKey refuses to start a multi-team controller that has a cache
// but no grant key of its own. The grant is the only boundary between teams
// inside the cache, and without a usable key every run's grant request fails
// at request time instead of when the operator deploys. A single-team install
// keeps its request-time answer.
func checkCacheGrantKey(srv *controller.Server, cacheURL, cachePodURL, grantKey, cacheToken string) error {
	if !srv.MultiTeam() || (cacheURL == "" && cachePodURL == "") {
		return nil
	}
	if grantKey == "" {
		return errors.New("the license allows more than one team and a cache is configured, so " +
			authwire.CacheGrantKeyEnv + " must hold the key the controller signs cache grants with " +
			"and the cache verifies them with (generate one with `openssl rand -base64 32`)")
	}
	if grantKey == cacheToken {
		return errors.New(authwire.CacheGrantKeyEnv + " equals the cache's operator token " +
			"(SPARKWING_CACHE_TOKEN); give the grant key a secret of its own, since any holder of " +
			"the operator token could otherwise mint a grant for any team")
	}
	return nil
}

// safety: a multi-team controller holds other people's credentials, so it
// refuses to start rather than store them as plaintext; a single-team
// install keeps starting without a key, as it always has.
func configureSecrets(ctx context.Context, srv *controller.Server, cipher *secrets.Cipher) error {
	if cipher == nil {
		if srv.MultiTeam() {
			return errors.New("the license allows more than one team, so stored secrets must be encrypted: " +
				"set SPARKWING_SECRETS_KEY or --secrets-key-file to a base64-encoded 32-byte key " +
				"(generate one with `openssl rand -base64 32`)")
		}
		return nil
	}
	// safety: a typed-nil *secrets.Cipher satisfies the interface and would register as non-nil at the handler's seam.
	srv.WithSecretsCipher(cipher)
	if _, err := srv.ResealStoredSecrets(ctx); err != nil {
		return err
	}
	return nil
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

// safety: an unreadable page budget must not silently become the default, because
// the operator set it to cover a bucket the default cannot walk.
func envMeasurePages() int {
	raw := strings.TrimSpace(os.Getenv("SPARKWING_OBJECT_STORE_BUCKET_MEASURE_PAGES"))
	if raw == "" {
		return s3store.DefaultMaxUsagePages
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		fmt.Fprintf(os.Stderr,
			"sparkwing-controller: SPARKWING_OBJECT_STORE_BUCKET_MEASURE_PAGES=%q is not a page count; keeping %d\n",
			raw, s3store.DefaultMaxUsagePages)
		return s3store.DefaultMaxUsagePages
	}
	return n
}

// safety: a negative bound would silently remove the ceiling it names, so it stops the controller instead.
func applyBucketCeiling(cfg objectguard.CeilingConfig) error {
	for name, n := range map[string]int64{
		"--max-bucket-bytes":    cfg.Limit.MaxBytes,
		"--max-bucket-objects":  cfg.Limit.MaxObjects,
		"--warn-bucket-bytes":   cfg.Limit.WarnBytes,
		"--warn-bucket-objects": cfg.Limit.WarnObjects,
	} {
		if n < 0 {
			return fmt.Errorf("%s must not be negative; pass 0 to leave the bucket unlimited", name)
		}
	}
	if cfg.Reconcile < 0 {
		return fmt.Errorf("--bucket-reconcile must not be negative; pass 0 to measure the bucket only at startup")
	}
	limiter, err := objectguard.Shared()
	if err != nil {
		return err
	}
	limiter.Ceiling().Configure(cfg)
	return nil
}
