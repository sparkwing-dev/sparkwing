package deployment_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
)

func signed(t *testing.T, now time.Time, features ...string) deployment.Inputs {
	t.Helper()
	pub, priv := licensetest.NewKey(t)
	return deployment.Inputs{LicenseKey: pub, Now: now, License: licensetest.Sign(t, priv, licensetest.Terms{
		Features: features, IssuedTo: "test", IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	})}
}

func TestParseDeclaresTheKindOnce(t *testing.T) {
	now := time.Now()
	multi := signed(t, now, license.FeatureMultiTeam)
	team := multi
	team.Kind = "team"
	cfg, err := deployment.Parse(team)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kind() != deployment.KindTeam || !cfg.MultiTeam() || !cfg.Metering() || cfg.DeploymentBearer() {
		t.Fatalf("team deployment = kind %s multi %v metering %v bearer %v", cfg.Kind(), cfg.MultiTeam(), cfg.Metering(), cfg.DeploymentBearer())
	}
	// safety: the kind is what Parse saw, whatever the clock says later.
	if problem := cfg.LicenseProblem(now.Add(2 * time.Hour)); !strings.Contains(problem, "expired") {
		t.Fatalf("expired license problem = %q", problem)
	}
	if !cfg.MultiTeam() || !cfg.Metering() {
		t.Fatal("expiry changed the declared deployment")
	}
	if cfg.LicenseProblem(now) != "" {
		t.Fatal("a valid license reported a problem")
	}

	metered := signed(t, now, license.FeatureMetering)
	cfg, err = deployment.Parse(metered)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MultiTeam() || !cfg.Metering() || cfg.Kind() != deployment.KindLocal {
		t.Fatalf("metering-only local deployment: multi %v metering %v", cfg.MultiTeam(), cfg.Metering())
	}

	var zero deployment.Config
	if zero.Kind() != deployment.KindLocal || zero.Store() != deployment.StoreSQLite || zero.MultiTeam() || zero.Metering() || !zero.DeploymentBearer() {
		t.Fatal("the zero deployment is not a local one")
	}
	if _, ok := zero.PublicOrigin(); ok {
		t.Fatal("the zero deployment has a public origin")
	}
}

func TestParseRefusesAMisdeclaredDeployment(t *testing.T) {
	now := time.Now()
	multi := signed(t, now, license.FeatureMultiTeam)
	metering := signed(t, now, license.FeatureMetering)
	expired := signed(t, now.Add(-3*time.Hour), license.FeatureMultiTeam)
	expired.Now = now
	with := func(in deployment.Inputs, edit func(*deployment.Inputs)) deployment.Inputs {
		edit(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   deployment.Inputs
		want string
	}{
		"multi-team license on a local deployment": {multi, "--deployment=team"},
		"team without a license":                   {deployment.Inputs{Kind: "team"}, "license"},
		"team with a metering-only license":        {with(metering, func(in *deployment.Inputs) { in.Kind = "team" }), "multi-team"},
		"team with an expired license":             {with(expired, func(in *deployment.Inputs) { in.Kind = "team" }), "expired"},
		"cloud on sqlite":                          {with(multi, func(in *deployment.Inputs) { in.Kind = "cloud" }), "--store=postgres"},
		"cloud without an origin": {with(multi, func(in *deployment.Inputs) {
			in.Kind, in.Store, in.PGURL = "cloud", "postgres", "postgres://db"
		}), "--external-url"},
		"postgres without pg-url":     {deployment.Inputs{Store: "postgres"}, "pg-url"},
		"pg-url under sqlite":         {deployment.Inputs{PGURL: "postgres://db"}, "--store=postgres"},
		"unknown kind":                {deployment.Inputs{Kind: "hosted"}, "--deployment"},
		"unknown store":               {deployment.Inputs{Store: "mysql"}, "--store"},
		"unknown payment mode":        {deployment.Inputs{PaymentMode: "prod"}, "--payment-mode"},
		"origin with a query":         {deployment.Inputs{PublicOrigin: "https://x.test/?a=b"}, "--external-url"},
		"origin runners would refuse": {deployment.Inputs{PublicOrigin: "http://controller.example.com"}, "unencrypted"},
		"relative origin":             {deployment.Inputs{PublicOrigin: "x.test"}, "--external-url"},
		"cloud limits off the cloud":  {deployment.Inputs{LimitsCloud: true}, "--deployment=cloud"},
		"previous secrets key alone":  {deployment.Inputs{SecretsPreviousKey: []byte("k")}, "secrets-key.previous"},
		"published oidc key alone":    {deployment.Inputs{OIDCPublishedKey: []byte("k")}, "oidc-key.published"},
		"previous webhook alone":      {deployment.Inputs{PreviousWebhookSecret: []byte("k")}, "github-app-webhook-secret.previous"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := deployment.Parse(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse = %v, want a refusal naming %q", err, tc.want)
			}
		})
	}
}

func TestParseAcceptsTheHostedDeployment(t *testing.T) {
	in := signed(t, time.Now(), license.FeatureMultiTeam)
	in.Kind, in.Store, in.PGURL, in.PublicOrigin, in.PaymentMode, in.LimitsCloud = "cloud", "postgres", "postgres://db", "https://api.sparkwing.dev/", "live", true
	cfg, err := deployment.Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	origin, ok := cfg.PublicOrigin()
	if !cfg.Hosted() || !cfg.DeploymentBearer() || cfg.Store() != deployment.StorePostgres || cfg.PGURL() != "postgres://db" ||
		!ok || origin != "https://api.sparkwing.dev" || cfg.PaymentMode() != "live" {
		t.Fatalf("hosted deployment = %+v", cfg)
	}
}

// A typo such as http://api.sparkwing.dev must not send a runner's machine
// token in clear text; in-cluster and loopback controllers stay on http.
func TestControllerTransportRefusesPlainHTTPOffTheCluster(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://api.sparkwing.dev":                               true,
		"http://127.0.0.1:4344":                                   true,
		"http://[::1]:4344":                                       true,
		"http://localhost:4344":                                   true,
		"http://sparkwing-controller.sparkwing.svc":               true,
		"http://sparkwing-controller.sparkwing.svc.cluster.local": true,
		"http://api.sparkwing.dev":                                false,
		"http://10.0.0.5:4344":                                    false,
		"http://controller.svc.example.com":                       false,
		"http://localhost.example.com":                            false,
	} {
		if err := deployment.CheckControllerURL("--controller", raw); (err == nil) != ok {
			t.Errorf("%s: err = %v, want allowed %v", raw, err, ok)
		}
	}
}
