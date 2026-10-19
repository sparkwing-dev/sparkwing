// Package deployment is the controller's declared deployment: what kind of
// deployment this is, which store it keeps state in, where it is reached
// from, how it settles payments and which keys it holds. The controller
// parses it once at startup and keeps it for the life of the process, so no
// later request, clock reading or missing file can change what kind of
// deployment answers.
package deployment

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/license"
)

// Kind is what kind of deployment a controller serves.
type Kind string

// The deployment kinds an operator declares.
const (
	// KindLocal holds one team and runs without a multi-team license.
	KindLocal Kind = "local"
	// KindTeam is a self-hosted controller holding many teams under a
	// verified multi-team license, with metering on.
	KindTeam Kind = "team"
	// KindCloud is the hosted service: many teams, metering, and the
	// deployment bearer kept for its operator.
	KindCloud Kind = "cloud"
)

// kinds lists every declared kind in the order the flag documents them.
func kinds() []Kind { return []Kind{KindLocal, KindTeam, KindCloud} }

// Store is the database a controller keeps its state in.
type Store string

// The stores an operator declares.
const (
	StoreSQLite   Store = "sqlite"
	StorePostgres Store = "postgres"
)

// stores lists every declared store.
func stores() []Store { return []Store{StoreSQLite, StorePostgres} }

// KeyPair is a current key and the previous one kept for reading values
// written before a rotation. A previous key without a current one is
// refused at construction, because it would read old values and leave
// nothing to write new ones under.
type KeyPair struct {
	current, previous []byte
}

// NewKeyPair pairs current and previous. Names are the credential file
// names an operator sees in the refusal.
func NewKeyPair(currentName, previousName string, current, previous []byte) (KeyPair, error) {
	if current == nil && previous != nil {
		return KeyPair{}, fmt.Errorf("the %s credential is present without %s; add the key in use now", previousName, currentName)
	}
	return KeyPair{current: current, previous: previous}, nil
}

// Current is the key in use now, or nil when none is configured.
func (k KeyPair) Current() []byte { return k.current }

// Previous is the key kept from before the last rotation, or nil.
func (k KeyPair) Previous() []byte { return k.previous }

// Inputs is everything the parser reads: flag values, credential contents
// and the key licenses are verified against.
type Inputs struct {
	Kind         string
	Store        string
	PublicOrigin string
	PaymentMode  string
	LimitsCloud  bool
	PGURL        string
	License      string
	LicenseKey   ed25519.PublicKey
	Now          time.Time

	SecretsKey, SecretsPreviousKey       []byte
	OIDCKey, OIDCPublishedKey            []byte
	WebhookSecret, PreviousWebhookSecret []byte
}

// Config is a parsed, validated deployment. The zero value is a local
// deployment on SQLite with nothing else configured.
type Config struct {
	kind         Kind
	store        Store
	pgURL        string
	publicOrigin string
	paymentMode  capability.PaymentMode
	license      *license.License
	metering     bool

	secretsKey, oidcKey, webhookSecret KeyPair
}

// Parse validates in and returns the deployment it declares. Every refusal
// names the flag or credential to change.
func Parse(in Inputs) (Config, error) {
	var c Config
	var err error
	if c.kind, err = parseKind(in.Kind); err != nil {
		return Config{}, err
	}
	if c.store, err = parseStore(in.Store); err != nil {
		return Config{}, err
	}
	switch {
	case c.store == StorePostgres && in.PGURL == "":
		return Config{}, errors.New("--store=postgres needs the pg-url credential in --credentials-dir")
	case c.store == StoreSQLite && in.PGURL != "":
		return Config{}, errors.New("the pg-url credential is present but --store is sqlite; set --store=postgres to use it, or remove the file")
	case c.kind == KindCloud && c.store != StorePostgres:
		return Config{}, errors.New("--deployment=cloud keeps its state in PostgreSQL; set --store=postgres")
	}
	c.pgURL = in.PGURL
	if in.LimitsCloud && c.kind != KindCloud {
		return Config{}, errors.New("a cloud limits profile belongs to the hosted service; set --deployment=cloud or choose another --limits-profile")
	}
	if c.publicOrigin, err = parsePublicOrigin(in.PublicOrigin); err != nil {
		return Config{}, err
	}
	if c.kind == KindCloud && c.publicOrigin == "" {
		return Config{}, errors.New("--deployment=cloud needs --external-url, the https origin runners and GitHub reach it on")
	}
	switch mode := capability.PaymentMode(in.PaymentMode); mode {
	case "", capability.PaymentTest, capability.PaymentLive:
		c.paymentMode = mode
	default:
		return Config{}, fmt.Errorf("--payment-mode %q is not test or live", in.PaymentMode)
	}
	if err := c.verifyLicense(in.License, in.LicenseKey, in.Now); err != nil {
		return Config{}, err
	}
	if c.secretsKey, err = NewKeyPair("secrets-key", "secrets-key.previous", in.SecretsKey, in.SecretsPreviousKey); err != nil {
		return Config{}, err
	}
	if c.oidcKey, err = NewKeyPair("oidc-key", "oidc-key.published", in.OIDCKey, in.OIDCPublishedKey); err != nil {
		return Config{}, err
	}
	if c.webhookSecret, err = NewKeyPair("github-app-webhook-secret", "github-app-webhook-secret.previous", in.WebhookSecret, in.PreviousWebhookSecret); err != nil {
		return Config{}, err
	}
	return c, nil
}

func parseKind(raw string) (Kind, error) {
	if raw == "" {
		return KindLocal, nil
	}
	for _, k := range kinds() {
		if Kind(raw) == k {
			return k, nil
		}
	}
	return "", fmt.Errorf("--deployment %q is not local, team or cloud", raw)
}

func parseStore(raw string) (Store, error) {
	if raw == "" {
		return StoreSQLite, nil
	}
	for _, s := range stores() {
		if Store(raw) == s {
			return s, nil
		}
	}
	return "", fmt.Errorf("--store %q is not sqlite or postgres", raw)
}

func parsePublicOrigin(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("--external-url %q is not an absolute http(s) URL without credentials, query or fragment", raw)
	}
	return raw, nil
}

// safety: the license decides the kind only at startup. A license that
// stops verifying later leaves the kind as declared and shows in health,
// so expiry can never silently switch metering or sign-in off.
func (c *Config) verifyLicense(raw string, key ed25519.PublicKey, now time.Time) error {
	if strings.TrimSpace(raw) == "" {
		if c.kind != KindLocal {
			return fmt.Errorf("--deployment=%s needs a multi-team license in the license credential", c.kind)
		}
		return nil
	}
	lic, err := license.Verify(raw, key, now)
	if err != nil {
		return fmt.Errorf("license credential: %w; fix or remove it", err)
	}
	multiTeam := lic.Allows(license.FeatureMultiTeam, now)
	switch {
	case c.kind == KindLocal && multiTeam:
		return errors.New("the license grants multi-team, so declare --deployment=team (or cloud); a local deployment holds one team")
	case c.kind != KindLocal && !multiTeam:
		return fmt.Errorf("--deployment=%s needs a license that grants multi-team; this one does not", c.kind)
	}
	c.license = lic
	c.metering = lic.Allows(license.FeatureMetering, now)
	return nil
}

// Kind is the declared deployment kind.
func (c Config) Kind() Kind {
	if c.kind == "" {
		return KindLocal
	}
	return c.kind
}

// Store is the declared state store.
func (c Config) Store() Store {
	if c.store == "" {
		return StoreSQLite
	}
	return c.store
}

// PGURL is the PostgreSQL connection string when Store is postgres.
func (c Config) PGURL() string { return c.pgURL }

// MultiTeam reports whether the deployment holds more than one team.
func (c Config) MultiTeam() bool { return c.Kind() != KindLocal }

// Metering reports whether usage is charged to credits. It is fixed at
// startup from the license that verified then.
func (c Config) Metering() bool { return c.metering }

// Hosted reports whether this is the hosted service.
func (c Config) Hosted() bool { return c.Kind() == KindCloud }

// DeploymentBearer reports whether a deployment administrator's bearer
// token may use the routes that accept one. A self-hosted multi-team
// deployment administers through signed-in operator accounts instead.
func (c Config) DeploymentBearer() bool { return c.Kind() != KindTeam }

// PublicOrigin is the externally reachable base URL, without a trailing
// slash, and whether one was declared. Nothing derives it from a request.
func (c Config) PublicOrigin() (string, bool) { return c.publicOrigin, c.publicOrigin != "" }

// PaymentMode is the declared Stripe mode, empty when settlement is off.
func (c Config) PaymentMode() capability.PaymentMode { return c.paymentMode }

// SecretsKey is the stored-secret encryption key pair.
func (c Config) SecretsKey() KeyPair { return c.secretsKey }

// OIDCKey is the OIDC signing key and the previous key still published.
func (c Config) OIDCKey() KeyPair { return c.oidcKey }

// WebhookSecret is the GitHub App webhook secret pair.
func (c Config) WebhookSecret() KeyPair { return c.webhookSecret }

// LicenseProblem describes a license that has stopped being valid since
// startup, or returns "" while it holds. The deployment keeps its kind
// either way; the problem is for health and the operator's alarm.
func (c Config) LicenseProblem(now time.Time) string {
	if c.license == nil {
		return ""
	}
	if !c.license.Allows(license.FeatureMetering, now) {
		return "license: expired at " + c.license.ExpiresAt().UTC().Format(time.RFC3339) + "; renew it, because the next restart refuses to start"
	}
	return ""
}
