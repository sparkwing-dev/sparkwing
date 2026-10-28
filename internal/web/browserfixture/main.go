package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/license"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/store/teststore"
)

type fixtureConfig struct {
	home   string
	webOut string
}

type singleValue struct {
	name  string
	value string
	set   bool
}

func (v *singleValue) String() string { return v.value }

func (v *singleValue) Set(value string) error {
	if v.set {
		return fmt.Errorf("%s may only be set once", v.name)
	}
	if value == "" {
		return fmt.Errorf("%s requires a non-empty value", v.name)
	}
	v.value = value
	v.set = true
	return nil
}

func parseFixtureConfig(args []string) (fixtureConfig, error) {
	flags := flag.NewFlagSet("browserfixture", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	home := singleValue{name: "--fixture-home"}
	webOut := singleValue{name: "--web-out"}
	flags.Var(&home, "fixture-home", "temporary fixture state directory")
	flags.Var(&webOut, "web-out", "dashboard static export directory")
	if err := flags.Parse(args); err != nil {
		return fixtureConfig{}, fmt.Errorf("parse browser fixture flags: %w", err)
	}
	if flags.NArg() != 0 {
		return fixtureConfig{}, fmt.Errorf("unexpected browser fixture argument %q", flags.Arg(0))
	}
	if !home.set {
		return fixtureConfig{}, fmt.Errorf("--fixture-home is required")
	}
	if !webOut.set {
		return fixtureConfig{}, fmt.Errorf("--web-out is required")
	}
	return fixtureConfig{home: home.value, webOut: webOut.value}, nil
}

const fixtureTeam store.Team = "acme"

type seedRequest struct {
	Count       int       `json:"count"`
	Pipeline    string    `json:"pipeline"`
	Branch      string    `json:"branch"`
	Status      string    `json:"status"`
	Start       time.Time `json:"start"`
	StepSeconds int       `json:"step_seconds"`
	ID          string    `json:"id"`
	IDPrefix    string    `json:"id_prefix"`
	FirstIndex  int       `json:"first_index"`
}

func (r *seedRequest) normalize() error {
	if r.Pipeline == "" {
		r.Pipeline = "fixture"
	}
	if r.Branch == "" {
		r.Branch = "main"
	}
	if r.Status == "" {
		r.Status = "success"
	}
	if r.Start.IsZero() {
		r.Start = time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	}
	if r.StepSeconds == 0 {
		r.StepSeconds = 60
	}
	if r.IDPrefix == "" {
		r.IDPrefix = "seeded-"
	}
	switch {
	case r.Count < 1 || r.Count > 10000:
		return errors.New("count must be between 1 and 10000")
	case r.Status != "success" && r.Status != "failed":
		return errors.New("status must be success or failed")
	case r.StepSeconds < 1 || r.FirstIndex < 0:
		return errors.New("step_seconds must be positive and first_index not negative")
	case r.ID != "" && r.Count != 1:
		return errors.New("id names one run, so count must be 1")
	}
	return nil
}

func seedRuns(ctx context.Context, st *store.Store, r seedRequest) ([]string, error) {
	// hack: a real run finishes only by executing on a runner, so the fixture records the terminal reporting
	// facts the console reads instead of driving hundreds of executions.
	ids := make([]string, 0, r.Count)
	for i := range r.Count {
		index := r.FirstIndex + i
		started := r.Start.Add(time.Duration(index*r.StepSeconds) * time.Second).UTC()
		finished := started.Add(time.Duration(r.StepSeconds) * time.Second / 2)
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("%s%04d", r.IDPrefix, index)
		}
		if err := teststore.SeedRun(st, ctx, fixtureTeam, store.Run{RunSummary: runstate.RunSummary{
			ID: id, Pipeline: r.Pipeline, Status: r.Status, GitBranch: r.Branch,
			CreatedAt: started, StartedAt: started, FinishedAt: &finished,
		}}); err != nil {
			return ids, fmt.Errorf("seed run %s: %w", id, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// safety: the control listener is a second loopback server, so the dashboard's own origin answers only what a
// deployed controller answers. It counts and revokes only browser sign-in sessions, never the session the
// fixture's member token descends from.
func controlHandler(db *sql.DB, st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__fixture/state", func(w http.ResponseWriter, _ *http.Request) {
		var sessions int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE capability_prefix <> ''`).Scan(&sessions); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]int{"sessions": sessions}); err != nil {
			log.Printf("browser fixture state: %v", err)
		}
	})
	mux.HandleFunc("POST /__fixture/revoke", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := db.Exec(`DELETE FROM sessions WHERE capability_prefix <> ''`); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /__fixture/runs", func(w http.ResponseWriter, r *http.Request) {
		var request seedRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := request.normalize(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ids, err := seedRuns(r.Context(), st, request)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string][]string{"ids": ids}); err != nil {
			log.Printf("browser fixture runs: %v", err)
		}
	})
	return mux
}

func serve(listener net.Listener, handler http.Handler) *http.Server {
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("browser fixture server: %v", err)
		}
	}()
	return server
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	return b
}

func teamDeployment() (deployment.Config, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return deployment.Config{}, err
	}
	now := time.Now()
	body, err := json.Marshal(map[string]any{
		"features": []string{license.FeatureMultiTeam}, "issued_to": "browser fixture",
		"issued_at": now.Add(-time.Hour).UTC(), "expires_at": now.Add(24 * time.Hour).UTC(),
	})
	if err != nil {
		return deployment.Config{}, err
	}
	text := base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, body))
	return deployment.Parse(deployment.Inputs{
		Kind: string(deployment.KindTeam), License: text, LicenseKey: pub, Now: now, SecretsKey: randomBytes(secrets.KeySize),
	})
}

func memberToken(ctx context.Context, st *store.Store) (string, error) {
	// hack: a provider sign-in gives the new account its own team as owner, which is the fixture's one team.
	op := st.AsOperator()
	signIn, err := op.ResolveSignIn(ctx, store.SignInProfile{
		Provider: store.ProviderGitHub, Subject: "browser-fixture", Email: string(fixtureTeam) + "@example.test", EmailVerified: true,
		Name: "Acme Owner", ProviderAccountCreatedAt: time.Now().AddDate(-1, 0, 0),
	}, store.SignUpConditions{})
	if err != nil {
		return "", fmt.Errorf("sign in fixture account: %w", err)
	}
	if signIn.PersonalTeam != fixtureTeam {
		return "", fmt.Errorf("fixture account landed in team %q, want %q", signIn.PersonalTeam, fixtureTeam)
	}
	session, _, _, err := op.CreateAccountSession(ctx, signIn.Account, fixtureTeam, 24*time.Hour)
	if err != nil {
		return "", fmt.Errorf("open fixture account session: %w", err)
	}
	digest := sha256.Sum256([]byte(session))
	authority := capability.AuthorityRef{Kind: capability.AuthoritySession, ID: hex.EncodeToString(digest[:])}
	issued, err := op.ProvisionCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionMember, AllowTeamAdmin: true, Authority: &authority, Team: string(fixtureTeam),
		Subject: capability.Subject{Kind: capability.SubjectAccount, ID: signIn.Account.ID},
		Grants:  teststore.CredentialGrants(store.ScopesForRole(store.RoleOwner)), Binding: capability.Binding{Kind: capability.BindingNone},
	})
	if err != nil {
		return "", fmt.Errorf("mint fixture member token: %w", err)
	}
	return issued.Raw, nil
}

func main() {
	config, err := parseFixtureConfig(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(config.home, 0o700); err != nil {
		log.Fatal(err)
	}
	home := paths.PathsAt(filepath.Join(config.home, "home"))
	if err := home.EnsureRoot(); err != nil {
		log.Fatal(err)
	}
	cfg, err := teamDeployment()
	if err != nil {
		log.Fatal(err)
	}
	cipher, err := secrets.NewCipher(cfg.SecretsKey().Current())
	if err != nil {
		log.Fatal(err)
	}
	engines, err := engine.NewResolver(engine.Config{BuildVersion: "development", CacheDirectory: filepath.Join(home.Root, "engines")})
	if err != nil {
		log.Fatal(err)
	}
	dashboardListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	origin := "http://" + dashboardListener.Addr().String()
	objects, err := blob.NewFilesystem(blob.FilesystemOptions{Directory: filepath.Join(home.Root, "data"), Endpoint: origin, SigningKey: randomBytes(32)})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := objects.Close(); err != nil {
			log.Printf("browser fixture objects close: %v", err)
		}
	}()
	var srv *controller.Server
	st, err := store.Open(home.StateDB(), store.WithPolicies(func(st *store.Store) (store.Policies, error) {
		srv = controller.NewDeployment(st, cfg, nil).WithEngineController(controller.NewEngineController(st, controller.EngineController{
			RunnerPolicy:  &controller.SignedRunnerAdmission{Resolver: engines},
			VersionPolicy: &controller.SignedEngineVersionPolicy{Resolver: engines},
		})).WithSecretsCipher(cipher).WithDataObjects(objects)
		// safety: the fixture serves plain HTTP on loopback, so its cookies drop Secure, which the controller
		// command refuses for a multi-team deployment.
		srv.WithDashboard(controller.Dashboard{Bundle: os.DirFS(config.webOut), Version: "auth-browser-fixture", Paths: home, InsecureCookies: true})
		return srv.StorePolicies(), nil
	}))
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("browser fixture store close: %v", err)
		}
	}()
	teststore.RegisterSQLite(st, home.StateDB())
	ctx := context.Background()
	operatorToken := capability.ControllerPrefix + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	if _, err := st.AsOperator().EnsureBootstrapCapability(ctx, capability.ProvisionSpec{
		Kind: capability.ProvisionBootstrap, Seed: operatorToken, Subject: capability.Subject{Kind: capability.SubjectOperator, ID: "bootstrap:admin"}, Binding: capability.Binding{Kind: capability.BindingNone}, Grants: []capability.Grant{capability.OperatorTokens, capability.OperatorLimits, capability.OperatorObjectStore},
	}); err != nil {
		log.Fatal(err)
	}
	token, err := memberToken(ctx, st)
	if err != nil {
		log.Fatal(err)
	}

	dashboardServer := serve(dashboardListener, srv.Handler())
	// safety: the fixture inspects sessions through its own connection, so
	// the store keeps no raw handle.
	raw, err := sql.Open("sqlite", "file:"+home.StateDB()+"?_pragma=busy_timeout(30000)")
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := raw.Close(); err != nil {
			log.Printf("browser fixture sql close: %v", err)
		}
	}()
	controlListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		if err := dashboardServer.Close(); err != nil {
			log.Printf("browser fixture close: %v", err)
		}
		log.Fatal(err)
	}
	controlServer := serve(controlListener, controlHandler(raw, st))
	started := map[string]string{
		"origin":         origin,
		"control_origin": "http://" + controlListener.Addr().String(),
		"team":           string(fixtureTeam),
		"token":          token,
		"admin_token":    operatorToken,
	}
	if err := json.NewEncoder(os.Stdout).Encode(started); err != nil {
		log.Fatal(err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	<-signals
	for _, server := range []*http.Server{dashboardServer, controlServer} {
		if err := server.Close(); err != nil {
			log.Printf("browser fixture close: %v", err)
		}
	}
}
