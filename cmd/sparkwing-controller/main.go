package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/credentials"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/localsecrets"
	"github.com/sparkwing-dev/sparkwing/internal/logutil"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/internal/oidcissuer"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	s3store "github.com/sparkwing-dev/sparkwing/pkg/storage/s3"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/storeurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func main() {
	run := run
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "admin" {
		run = func(args []string) error { return runAdmin(args[1:], os.Stdout, os.Stderr) }
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, "sparkwing-controller:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("sparkwing-controller", flag.ContinueOnError)
	if len(args) == 1 && args[0] == "help" {
		args = []string{"--help"}
	}
	addr := fs.String("addr", "127.0.0.1:4344", "bind address")
	metricsAddr := fs.String("metrics-addr", "",
		"bind address for the Prometheus /metrics endpoint. Set it to move "+
			"/metrics off the API listener, and off any ingress fronting that "+
			"listener, onto its own port. Empty serves /metrics on --addr.")
	credentialsDir := fs.String(credentials.FlagName, "",
		"directory holding the controller's secrets, one file each under a fixed name: "+
			strings.Join([]string{
				credPGURL, credSecretsKey, credSecretsPreviousKey, credBootstrapAdminToken,
				credLicense, credOIDCKey, credOIDCPublishedKey, credGitHubClientSecret, credGoogleClientSecret,
				credGitHubAppKey, credGitHubAppWebhookSecret, credGitHubAppPreviousHook, credCloudFrontKey,
				credBillingToken, credStripeAPIKey, credStripeWebhookSecret,
			}, ", ")+
			". An absent file leaves its feature off; docs/self-hosting.md says what each one turns on.")
	dashboardURL := fs.String("dashboard-url", "",
		"externally-reachable URL of the dashboard that watches this controller. "+
			"Announced via GET /api/v1/services. Empty disables the announcement.")
	billingURL := fs.String("billing-url", "",
		"base URL of the hosted checkout service that opens a Stripe Checkout Session "+
			"when a team owner buys credits; the controller authenticates with the "+
			credBillingToken+" credential and names this deployment by the --external-url host. Empty sells no credits.")
	paymentMode := fs.String("payment-mode", "", "Stripe callback mode: test or live. Requires stripe-api-key and stripe-webhook-secret in --credentials-dir. Empty disables settlement.")
	operatorAccounts := fs.String("operator-accounts", "",
		"comma-separated account ids whose signed-in dashboard sessions may use the "+
			"operator console. No token reaches the console. Empty leaves it off.")
	externalURL := fs.String("external-url", "",
		"base URL this controller answers on from outside the cluster, which is "+
			"where runners transfer signed filesystem data and logs. "+
			"Empty uses --addr, with localhost for a wildcard listener.")
	oidcTokenTTL := fs.Duration("oidc-token-ttl", oidcissuer.DefaultTTL,
		"lifetime of an OIDC ID token, from 1m to 1h")
	trustedProxyCIDRs := fs.String("trusted-proxy-cidrs", "", "comma-separated proxy CIDRs allowed to supply X-Forwarded-For for request logs and per-IP limits; empty ignores it")
	trustedProxyAddr := fs.String("trusted-proxy-addr", "",
		"second address serving the same API, whose requests key login throttling, the bearer "+
			"failure budget and the request log on X-Real-IP. Only a proxy that overwrites "+
			"X-Real-IP may reach it. Empty serves only --addr, which ignores X-Real-IP")
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
	defaultPreferLabels := fs.String("default-prefer-labels", "",
		"comma-separated label terms a node with no Prefers of its own prefers "+
			"its runner to advertise, in the Prefers term syntax. Empty leaves "+
			"such a node to the first claimant")
	placementHold := fs.Duration("placement-hold", 20*time.Second,
		"how long a node is held back from a claim-mode runner that does not "+
			"advertise its preference, measured from the node's ready time, while "+
			"a runner that does advertise it is live and has a slot. Zero claims "+
			"first-in-first-out regardless of preference.")
	placementLiveness := fs.Duration("placement-liveness", 30*time.Second,
		"how recently a claim-mode runner must have polled for a claim to count "+
			"as live for the hold above")
	maxRunsPerPrincipalHour := fs.Int("max-runs-per-principal-hour", 0,
		"cap on the runs one principal may create in a rolling hour. Each run a "+
			"GitHub App delivery creates counts against the team the installation "+
			"is bound to. Past the cap the "+
			"controller answers 429 with a Retry-After and logs the principal and "+
			"the reason. The budget lives in controller memory, so a restart "+
			"refills every principal. Zero is unlimited.")
	shedQueueDepth := fs.Int("shed-queue-depth", 0,
		"pending-trigger depth of one team past which that team's new webhook "+
			"deliveries and API submissions are shed with 503 and a Retry-After rather than queued. "+
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
			"rolling minute, charged whatever the answer. Zero is unlimited; %d suits the "+
			"cadence the shipped runners heartbeat at.",
			controller.RecommendedHeartbeatsPerMinute))
	requestsPerTokenMinute := fs.Int(flagRequestsPerTokenMinute, 0,
		"request budget on every route one token can reach, per rolling minute, "+
			"keyed on the token prefix. It bounds a caller that varies the runner "+
			"it says it is, which the per-runner budgets above cannot. "+
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
			"alarm, the egress alarm, stream and download caps, and idle-poll enforcement, "+
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
	objectStoreBudget := fs.String("object-store-budget", "",
		"per-process object-store request budgets, as comma-separated class:window=count entries such as "+
			"put:minute=600,get:day=0, where class is put, get, list or delete, window is minute or day, and 0 "+
			"leaves that window unlimited. A class and window the list does not name keeps its built-in budget")
	maxBucketBytes := fs.Int64("max-bucket-bytes", 0,
		"stored bytes across the whole object store at or above which the controller freezes "+
			"object writes: existing runs finish, new writes are refused naming the ceiling, "+
			"and health reports the freeze. 0, the default, leaves the bucket unlimited "+
			"")
	maxBucketObjects := fs.Int64("max-bucket-objects", 0,
		"objects across the whole object store at or above which the controller freezes object "+
			"writes; 0 leaves the count unlimited")
	warnBucketBytes := fs.Int64("warn-bucket-bytes", 0,
		"stored bytes at which health reports the bucket as warning, which refuses nothing; "+
			"0 disables the warning")
	warnBucketObjects := fs.Int64("warn-bucket-objects", 0,
		"objects at which health reports the bucket as warning; 0 disables the warning "+
			"")
	bucketStoreURL := fs.String("bucket-store", "",
		"object store the bucket ceiling measures, as a store URL such as "+
			"s3://bucket/prefix. It is read on the reconciliation interval and never "+
			"served, so the controller exposes none of it. Empty counts only the writes "+
			"this process makes")
	freeTeamSlots := fs.Int64("free-team-slots", store.DefaultFreeTeamSlots,
		"teams without credits that may hold a free-tier slot on a multi-team controller. A team takes "+
			"one when it first starts a run and keeps it until the team is deleted, so free storage never "+
			"passes this many allowances; a team with neither credits nor a slot is refused. Lowering it "+
			"takes no slot back")
	cacheBlobStore := fs.String("cache-blob-store", "",
		"the cache's --blob-store, as s3://bucket/prefix. The hourly storage pass lists it to reconcile what each "+
			"team stores there and deletes a team's objects 30 days after they were last written; the operator's "+
			"team keeps its own. Empty leaves the cache's counts to its writes alone")
	logRetention := fs.String("log-retention", "",
		"teams whose node log segments keep for a set number of days instead of 90 (with credits) or 30 "+
			"(without), as team=days[,team=days...]. Each distinct number is a retention class under "+
			"<cache prefix>/logs/r<days>/ that needs its own lifecycle rule in the cache bucket")
	teamDownloadFree := fs.Int64("team-daily-download-free-bytes", controller.DefaultTeamDailyDownloadFreeBytes,
		"bytes one team without credits may download in a UTC day: binaries, artifacts, dependency archives "+
			"and git fetches, through the cache or this controller. Log reads are not counted. Past it the "+
			"team's next download is refused with 429 until midnight UTC; one already under way finishes. The "+
			"operator's team is exempt, and 0 turns the cap off")
	teamDownloadFunded := fs.Int64("team-daily-download-funded-bytes", controller.DefaultTeamDailyDownloadFundedBytes,
		"the same daily cap for a team with credits; 0 turns it off")
	bucketMeasurePages := fs.Int("bucket-measure-pages", s3store.DefaultMaxUsagePages,
		"listings one bucket measurement may spend before it stops and reports itself "+
			"incomplete. Each listing covers a thousand objects, so the default bounds a "+
			"measurement at a million; raise it for a larger bucket, or leave the ceiling "+
			"on its running count")
	bucketReconcile := fs.Duration("bucket-reconcile", objectguard.DefaultCeilingReconcile,
		"how often the controller measures the whole bucket and replaces the running "+
			"count with the measurement. Writes are counted as they happen, so this "+
			"listing is the only enumeration the ceiling costs; 0 measures once at "+
			"startup and never again")
	readEgress := egress.Bind(fs)
	readLog := logutil.Bind(fs)
	googleClientID := fs.String("google-client-id", "",
		"Google OAuth client id for dashboard sign-in; the secret is the "+
			credGoogleClientSecret+" credential. Offered only with a multi-team license.")
	githubClientID := fs.String("github-client-id", "",
		"GitHub OAuth app client id for dashboard sign-in; the secret is the "+
			credGitHubClientSecret+" credential. Offered only with a multi-team license.")
	emailSender := fs.String("email-sender", "",
		"address invitation emails are sent from through Amazon SES, such as noreply@example.com; "+
			"credentials and region come from the AWS default chain. Empty sends no email and logs "+
			"each invitation instead, so the owner shares its link")
	emailConfigSet := fs.String("email-configuration-set", "",
		"SES configuration set every invitation email names, for delivery and bounce events; empty names none")
	githubAppID := fs.String("github-app-id", "",
		"numeric id of the deployment's GitHub App. The App's client id and secret are "+
			"--github-client-id and the "+credGitHubClientSecret+" credential, its private key is the "+
			credGitHubAppKey+" credential, and its webhook secret the "+credGitHubAppWebhookSecret+" credential")
	githubAppSlug := fs.String("github-app-slug", "",
		"the GitHub App's name in https://github.com/apps/<slug>, where a team owner installs it")
	cloudFrontDomain := fs.String("cloudfront-domain", "",
		"CloudFront distribution domain that signs public-ingress downloads from --cache-blob-store, such as "+
			"d111111abcdef8.cloudfront.net; needs --cloudfront-key-pair-id and the "+credCloudFrontKey+" credential")
	cloudFrontKeyPairID := fs.String("cloudfront-key-pair-id", "",
		"CloudFront key pair or public key id the "+credCloudFrontKey+" credential signs for")
	oauthRedirectURIs := fs.String("oauth-redirect-uris", "",
		"comma-separated dashboard callback URLs a sign-in may return to, "+
			"such as https://app.example.com/auth/google/callback")
	signUpGate := fs.String("signup-gate", string(store.SignUpOpen),
		"open or waitlist. waitlist places every new account on the sign-up "+
			"waitlist whatever the operator's stored setting says; open defers to "+
			"that setting, which PUT /api/v1/operator/signups changes. Existing accounts are "+
			"never affected.")
	hsts := fs.Bool("hsts", false,
		"assert that browsers reach this controller's dashboard over TLS: send "+
			"Strict-Transport-Security and require an https origin on cookie-authenticated "+
			"writes. Unneeded when a proxy reaching --trusted-proxy-addr forwards X-Forwarded-Proto")
	insecureCookies := fs.Bool("insecure-cookies", false,
		"drop Secure and the __Host- prefix from the dashboard's session cookies so a browser "+
			"keeps a session over plain HTTP. Only for a single-team, self-hosted dashboard published without TLS: the "+
			"cookies then travel readable to every hop on the path")
	requireAuth := fs.Bool("require-auth", false,
		"refuse to start until a live deployment administrator capability exists; private routes always require credentials")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if fs.NArg() != 0 {
		fs.PrintDefaults()
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	proxyCIDRs, err := parseTrustedProxyCIDRs(*trustedProxyCIDRs)
	if err != nil {
		return err
	}

	creds, err := readCredentials(*credentialsDir)
	if err != nil {
		return err
	}
	paymentVerifier, err := readPaymentAuthenticator(*credentialsDir, *paymentMode)
	if err != nil {
		return fmt.Errorf("payment configuration: %w", err)
	}
	egressCfg, egressNamed, err := readEgress()
	if err != nil {
		return err
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
		EgressDailyAlarmBytes:     egressCfg.GlobalDailyAlarmBytes,
	}, guardsNamed{
		ClaimsPerRunnerMinute:     fs.Changed(flagClaimsPerRunnerMinute),
		HeartbeatsPerRunnerMinute: fs.Changed(flagHeartbeatsPerRunnerMinute),
		RequestsPerTokenMinute:    fs.Changed(flagRequestsPerTokenMinute),
		RequestsPerMinuteAlarm:    fs.Changed(flagRequestsPerMinuteAlarm),
		MaxLogStreamsPerPrincipal: egressNamed.MaxLogStreams,
		MaxDownloadsPerPrincipal:  egressNamed.MaxDownloads,
		RunsPerPrincipalHour:      fs.Changed("max-runs-per-principal-hour"),
		ShedQueueDepth:            fs.Changed("shed-queue-depth"),
		EgressDailyAlarmBytes:     egressNamed.DailyAlarmBytes,
	})
	egressCfg.MaxStreamsPerPrincipal = guards.MaxLogStreamsPerPrincipal
	egressCfg.MaxDownloadsPerPrincipal = guards.MaxDownloadsPerPrincipal
	egressCfg.GlobalDailyAlarmBytes = guards.EgressDailyAlarmBytes
	if int64(*liveLogNodeKB)<<10 > int64(*liveLogTotalMB)<<20 {
		return fmt.Errorf("--live-log-node-kb (%d) exceeds --live-log-total-mb (%d), so one node would never fit",
			*liveLogNodeKB, *liveLogTotalMB)
	}
	budget, err := objectguard.ParseBudget(*objectStoreBudget)
	if err != nil {
		return err
	}
	if err := objectguard.ConfigureShared(budget); err != nil {
		return err
	}
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
	st, serr := openControllerStore(context.Background(), p.StateDB(), creds.PGURL)
	if serr != nil {
		return fmt.Errorf("open state db: %w", serr)
	}
	defer func() { _ = st.Close() }()
	if paymentVerifier != nil {
		if err := st.AsOperator().SetPaymentMode(context.Background(), capability.PaymentMode(*paymentMode)); err != nil {
			return fmt.Errorf("persist payment configuration: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tel, err := otelutil.Init(ctx, otelutil.Config{ServiceName: "sparkwing-controller", Log: readLog()})
	if err != nil {
		return err
	}
	defer func() { _ = tel.Shutdown(context.Background()) }()

	bootstrapToken := creds.BootstrapAdminToken
	created, bterr := controller.EnsureBootstrapAdminToken(ctx, st, bootstrapToken)
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
				"sparkwing-controller: bootstrap admin token skipped: the capability bootstrap gate is closed")
		}
	}

	deployment, err := paymentDeployment(*externalURL, *billingURL, *paymentMode)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*billingURL) != "" && creds.BillingToken == "" {
		return errors.New("--billing-url is set but the " + credBillingToken + " credential is absent; " +
			"the checkout service refuses a controller without its token")
	}

	srv, err := newControllerServer(st, p.Root, readProvenance().Version, nil)
	if err != nil {
		return err
	}
	srv = srv.
		WithCloudDeployment(*limitsProfile == controller.LimitsProfileCloud || *limitsProfile == controller.LimitsProfileCloudFree).
		WithTrustedProxyAddr(*trustedProxyAddr).
		WithTrustedProxyCIDRs(proxyCIDRs).
		WithTeamDownloadCaps(*teamDownloadFree, *teamDownloadFunded).
		WithDashboardURL(*dashboardURL).
		WithBillingCheckout(*billingURL, creds.BillingToken).
		WithPaymentDeployment(deployment).
		WithPayments(paymentVerifier, st).
		WithOperatorAccounts(splitCSV(*operatorAccounts)).
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
		License:            creds.License,
		GoogleClientID:     *googleClientID,
		GoogleClientSecret: creds.GoogleClientSecret,
		GitHubClientID:     *githubClientID,
		GitHubClientSecret: creds.GitHubClientSecret,
		RedirectURIs:       *oauthRedirectURIs,
		SignUpGate:         *signUpGate,
	}, slog.Default()); err != nil {
		return err
	}
	if err := configureMailer(ctx, srv, *emailSender, *emailConfigSet); err != nil {
		return err
	}
	if err := configureGitHubApp(srv, githubAppFlags{
		AppID:         *githubAppID,
		Slug:          *githubAppSlug,
		ClientID:      *githubClientID,
		ClientSecret:  creds.GitHubClientSecret,
		PrivateKey:    creds.GitHubAppKey,
		WebhookSecret: creds.GitHubAppWebhookSecret,
		PreviousHook:  creds.GitHubAppPreviousHook,
	}); err != nil {
		return err
	}
	oidcIssuer, oerr := loadOIDCIssuer(creds.OIDCKey, creds.OIDCPublishedKey, *externalURL, *oidcTokenTTL, os.Stderr)
	if oerr != nil {
		return fmt.Errorf("oidc issuer: %w", oerr)
	}
	srv = srv.WithOIDCIssuer(oidcIssuer)
	if err := checkMultiTeamObjectStore(srv, *bucketStoreURL); err != nil {
		return err
	}
	if err := checkInsecureCookies(srv, *limitsProfile == controller.LimitsProfileCloud || *limitsProfile == controller.LimitsProfileCloudFree, *insecureCookies); err != nil {
		return err
	}
	if *teamDownloadFree < 0 || *teamDownloadFunded < 0 {
		return fmt.Errorf("a team daily download cap must not be negative; pass 0 to turn it off")
	}
	if *freeTeamSlots < 0 {
		return fmt.Errorf("--free-team-slots must not be negative; pass 0 to admit no team without credits")
	}
	if err := st.SetFreeTeamSlots(ctx, *freeTeamSlots); err != nil {
		return fmt.Errorf("--free-team-slots: %w", err)
	}
	if err := configureSecrets(ctx, srv, st, creds, generatedKeyPath(p.StateDB())); err != nil {
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
	if *cacheBlobStore != "" && !strings.HasPrefix(*cacheBlobStore, "s3://") {
		return errors.New("--cache-blob-store must be an s3:// URL")
	}
	if (*cloudFrontDomain != "" || *cloudFrontKeyPairID != "" || creds.CloudFrontKey != "") && !strings.HasPrefix(*cacheBlobStore, "s3://") {
		return errors.New("CloudFront downloads require an S3 data store")
	}

	retention, err := parseLogRetention(*logRetention)
	if err != nil {
		return fmt.Errorf("--log-retention: %w", err)
	}
	srv = srv.WithLogRetention(retention)
	if strings.HasPrefix(*cacheBlobStore, "s3://") {
		objects, err := openS3Data(ctx, *cacheBlobStore)
		if err != nil {
			return fmt.Errorf("--cache-blob-store: direct uploads: %w", err)
		}
		var backend blob.ObjectBackend = objects
		if *cloudFrontDomain != "" || *cloudFrontKeyPairID != "" || creds.CloudFrontKey != "" {
			backend, err = controller.NewCloudFrontDataBackend(objects, *cloudFrontDomain, *cloudFrontKeyPairID, creds.CloudFrontKey)
			if err != nil {
				return err
			}
		}
		srv = srv.WithDataObjects(backend)
	} else {
		objects, err := openFilesystemData(p.Root, *externalURL, *addr)
		if err != nil {
			return fmt.Errorf("filesystem data: %w", err)
		}
		defer func() {
			if err := objects.Close(); err != nil {
				slog.Error("close filesystem data", "err", err)
			}
		}()
		srv = srv.WithDataObjects(objects)
	}
	adminReader, _ := any(st).(deploymentAdminReader)
	if err := checkRequireAuth(ctx, adminReader, *requireAuth); err != nil {
		return err
	}
	srv.WithDashboard(dashboard(p, *hsts, *insecureCookies))
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
	EgressDailyAlarmBytes     int64
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
	EgressDailyAlarmBytes     bool
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
	if !named.EgressDailyAlarmBytes {
		set.EgressDailyAlarmBytes = profile.EgressDailyAlarmBytes
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

type deploymentAdminReader interface {
	HasDeploymentAdministrator(context.Context) (bool, error)
}

func checkRequireAuth(ctx context.Context, reader deploymentAdminReader, required bool) error {
	if !required {
		return nil
	}
	if reader == nil {
		return errors.New("--require-auth: administrator capability check is unavailable")
	}
	ready, err := reader.HasDeploymentAdministrator(ctx)
	if err != nil {
		return fmt.Errorf("--require-auth: read deployment administrator capability: %w", err)
	}
	if !ready {
		return errors.New("--require-auth: no live deployment administrator capability; supply the " + credBootstrapAdminToken + " credential")
	}
	return nil
}

// safety: a free team is held to its allowance only by counters kept over the object
// store; the disk-backed cache and log volume enforce no allowance at all.
func checkMultiTeamObjectStore(srv *controller.Server, bucketStoreURL string) error {
	if !srv.MultiTeam() || strings.TrimSpace(bucketStoreURL) != "" {
		return nil
	}
	return errors.New("the license allows more than one team, so an object store is required: set --bucket-store " +
		"to the s3:// store the cache (--blob-store) keeps its objects in, because free-tier allowances are enforced only there")
}

func checkInsecureCookies(srv *controller.Server, hosted, insecure bool) error {
	if !insecure || !hosted && !srv.MultiTeam() {
		return nil
	}
	return errors.New("--insecure-cookies is refused on a multi-team or hosted controller, whose sessions belong to other people; " +
		"publish the dashboard over TLS and drop the flag")
}

// safety: no secret is stored as plaintext; a multi-team controller holds other
// people's credentials, so it refuses to start keyless, and a PostgreSQL one has
// no volume to keep a generated key on, so it starts and refuses secret writes.
func configureSecrets(ctx context.Context, srv *controller.Server, st *store.Store,
	creds controllerCredentials, keyPath string,
) error {
	cipher, err := secretsCipher(creds.SecretsKey, creds.SecretsPreviousKey)
	if err != nil {
		return err
	}
	switch {
	case cipher != nil:
		// safety: a typed-nil *secrets.Cipher satisfies the interface and would register as non-nil at the handler's seam.
		srv.WithSecretsCipher(cipher)
	case srv.MultiTeam():
		return errors.New("the license allows more than one team, so stored secrets must be encrypted: " +
			"put a base64-encoded 32-byte key in the " + credSecretsKey + " credential " +
			"(generate one with `openssl rand -base64 32`)")
	case st.Dialect() == store.DialectSQLite:
		ring, err := localsecrets.NewStoreKeyring(keyPath, nil,
			"Restore that key to "+keyPath+", or put it in the "+credSecretsKey+" credential, and start again.")
		if err != nil {
			return fmt.Errorf("secrets key %s: %w", keyPath, err)
		}
		if err := ring.MissingKey(ctx, st); err != nil {
			return err
		}
		srv.WithSecretsCipher(ring.For(st))
	default:
		srv.WithSecretsCipher(keylessCipher{})
		return nil
	}
	return nil
}

// safety: the generated key sits beside the database on its volume, so a
// backup or restore of one carries the other.
func generatedKeyPath(stateDB string) string {
	return filepath.Join(filepath.Dir(stateDB), "secrets.key")
}

func secretsCipher(key, previous []byte) (*secrets.Cipher, error) {
	if key == nil {
		if previous != nil {
			return nil, errors.New("the " + credSecretsPreviousKey + " credential is present without " +
				credSecretsKey + "; add the key values are sealed under now")
		}
		return nil, nil
	}
	if previous == nil {
		return secrets.NewCipher(key)
	}
	return secrets.NewCipherWithPrevious(key, previous)
}

func configureMailer(ctx context.Context, srv *controller.Server, sender, configSet string) error {
	if strings.TrimSpace(sender) == "" {
		return nil
	}
	m, err := mailer.NewSES(ctx, mailer.SESConfig{From: strings.TrimSpace(sender), ConfigurationSet: strings.TrimSpace(configSet)})
	if err != nil {
		return fmt.Errorf("--email-sender: %w", err)
	}
	srv.WithMailer(m)
	return nil
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
	objectguard.Shared().Ceiling().Configure(cfg)
	return nil
}

// safety: a source build carries no dashboard bundle, so the controller still starts and serves its API and
// sign-in pages; its dashboard pages name the build step instead.
func dashboard(p paths.Paths, hsts, insecureCookies bool) controller.Dashboard {
	d := controller.Dashboard{
		Version:         Version,
		HSTS:            hsts,
		InsecureCookies: insecureCookies,
		Paths:           p,
		Capabilities: backend.Capabilities{
			Mode:     "cluster",
			Storage:  backend.CapabilitiesStorage{Artifacts: "custom", Logs: "segments", Runs: "controller"},
			Features: []string{"pipelines", "runs", "logs", "secrets", "approvals", "cross-pipeline-refs"},
		},
	}
	if web.VerifyBundleEmbedded() == nil {
		d.Bundle = web.BundleFS()
	} else {
		fmt.Fprintln(os.Stderr, "sparkwing-controller: this build carries no dashboard bundle; "+
			"dashboard pages answer 503 until a build that ran bin/build-web.sh is deployed")
	}
	return d
}

func parseLogRetention(raw string) (map[store.Team]int, error) {
	out := map[store.Team]int{}
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		team, rawDays, ok := strings.Cut(item, "=")
		days, err := strconv.Atoi(rawDays)
		if !ok || err != nil || days < 1 || !blob.ValidLogTeam(team) {
			return nil, fmt.Errorf("%q is not team=days with a team slug and a positive number of days", item)
		}
		out[store.Team(team)] = days
	}
	return out, nil
}

func parseTrustedProxyCIDRs(value string) ([]netip.Prefix, error) {
	if value == "" {
		return nil, nil
	}
	var prefixes []netip.Prefix
	for _, raw := range strings.Split(value, ",") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil || prefix.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid --trusted-proxy-cidrs entry %q: use an IPv4 or IPv6 CIDR", raw)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}
