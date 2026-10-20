// Package deploymenttest parses declared deployments for tests, signing any
// license they need with a key the test generates, so a suite exercises the
// same parser the controller starts with.
package deploymenttest

import (
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/license/licensetest"
)

// Parse fills in.License with a license granting features, valid for a day
// around in.Now (or now), then parses in. No features leaves the license out.
func Parse(t testing.TB, in deployment.Inputs, features ...string) deployment.Config {
	t.Helper()
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	if len(features) > 0 {
		pub, priv := licensetest.NewKey(t)
		in.LicenseKey = pub
		in.License = licensetest.Sign(t, priv, licensetest.Terms{
			Features: features, IssuedTo: "test",
			IssuedAt: in.Now.Add(-time.Hour), ExpiresAt: in.Now.Add(24 * time.Hour),
		})
	}
	cfg, err := deployment.Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Team is a self-hosted multi-team deployment.
func Team(t testing.TB) deployment.Config {
	t.Helper()
	return Parse(t, deployment.Inputs{Kind: string(deployment.KindTeam)}, license.FeatureMultiTeam)
}

// Cloud is the hosted deployment, reached at origin. Its store is declared
// PostgreSQL whatever store the test opens, since the server never reads it.
func Cloud(t testing.TB, origin string) deployment.Config {
	t.Helper()
	return Parse(t, deployment.Inputs{
		Kind: string(deployment.KindCloud), Store: string(deployment.StorePostgres),
		PGURL: "postgres://test.invalid/sparkwing", PublicOrigin: origin,
	}, license.FeatureMultiTeam)
}

// Metered is a one-team deployment whose license grants only metering.
func Metered(t testing.TB) deployment.Config {
	t.Helper()
	return Parse(t, deployment.Inputs{}, license.FeatureMetering)
}

// Local is a one-team deployment reached at origin, which may be empty.
func Local(t testing.TB, origin string) deployment.Config {
	t.Helper()
	return Parse(t, deployment.Inputs{PublicOrigin: origin})
}
