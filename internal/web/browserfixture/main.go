package main

import (
	"context"
	"encoding/json"
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

	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
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

// safety: the control listener is a second loopback server, so the dashboard's own origin answers only what a
// deployed controller answers.
func controlHandler(st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__fixture/state", func(w http.ResponseWriter, _ *http.Request) {
		var sessions int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]int{"sessions": sessions}); err != nil {
			log.Printf("browser fixture state: %v", err)
		}
	})
	mux.HandleFunc("POST /__fixture/revoke", func(w http.ResponseWriter, _ *http.Request) {
		if _, err := st.DB().Exec(`DELETE FROM sessions`); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func listen(handler http.Handler) (net.Listener, *http.Server, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("browser fixture server: %v", err)
		}
	}()
	return listener, server, nil
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
	st, err := store.Open(home.StateDB())
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("browser fixture store close: %v", err)
		}
	}()
	now := time.Now().UTC()
	token, _, err := st.CreateToken("browser-fixture", store.TokenKindUser, []string{controller.ScopeAdmin}, 0, now)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := st.CreateUser(fixtureUser, fixturePassword, []string{controller.ScopeAdmin}, now); err != nil {
		log.Fatal(err)
	}
	srv := controller.New(st, nil).EnableAuthFromStore().WithDashboard(controller.Dashboard{
		Bundle:  os.DirFS(config.webOut),
		Version: "auth-browser-fixture",
		Paths:   home,
		// safety: the fixture serves plain HTTP on loopback, so its cookies drop Secure.
		InsecureCookies: true,
	})
	defer func() {
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("browser fixture shutdown: %v", err)
		}
	}()

	dashboardListener, dashboardServer, err := listen(srv.Handler())
	if err != nil {
		log.Fatal(err)
	}
	controlListener, controlServer, err := listen(controlHandler(st))
	if err != nil {
		if err := dashboardServer.Close(); err != nil {
			log.Printf("browser fixture close: %v", err)
		}
		log.Fatal(err)
	}
	started := map[string]string{
		"origin":         "http://" + dashboardListener.Addr().String(),
		"control_origin": "http://" + controlListener.Addr().String(),
		"admin_token":    token,
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

const (
	fixtureUser     = "admin"
	fixturePassword = "correct-horse"
)
