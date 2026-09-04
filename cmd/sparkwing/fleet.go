package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runFleet(args []string) error {
	if handleParentHelp(cmdFleet, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdFleet, os.Stdout)
		return nil
	}
	if args[0] == "init" {
		return runFleetInit(args[1:])
	}
	if args[0] != "agents" {
		return fmt.Errorf("fleet: unknown subcommand %q", args[0])
	}
	if len(args) == 1 || handleParentHelp(cmdFleetAgents, args[1:]) {
		PrintHelp(cmdFleetAgents, os.Stdout)
		return nil
	}
	if args[1] != "enroll" {
		return fmt.Errorf("fleet agents: unknown subcommand %q", args[1])
	}
	return runFleetAgentsEnroll(args[2:])
}

func runFleetInit(args []string) error {
	fs := flag.NewFlagSet(cmdFleetInit.Path, flag.ContinueOnError)
	listen := fs.String("listen", "", "fixed private listener address")
	publicURL := fs.String("public-url", "", "helper-reachable coordinator origin")
	allowTailnetHTTP := fs.Bool("allow-tailnet-http", false, "allow verified literal Tailscale-IP HTTP transport")
	tailnet := fs.Bool("tailnet", false, "configure direct Tailscale transport on port 4346")
	if err := parseAndCheck(cmdFleetInit, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	path := os.Getenv("SPARKWING_FLEET_CONFIG")
	var err error
	if path == "" {
		path, err = fleet.DefaultPath()
		if err != nil {
			return err
		}
	}
	cfg := fleet.Config{Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"}}
	if *tailnet {
		if *listen != "" || *publicURL != "" || *allowTailnetHTTP {
			return errors.New("fleet init: --tailnet is mutually exclusive with --listen, --public-url, and --allow-tailnet-http")
		}
		ips, err := fleet.LocalTailscaleIPs()
		if err != nil {
			return fmt.Errorf("fleet init --tailnet: %w", err)
		}
		cfg, err = directTailnetFleetConfig(ips)
		if err != nil {
			return fmt.Errorf("fleet init --tailnet: %w", err)
		}
	} else {
		if *listen == "" || *publicURL == "" {
			return errors.New("fleet init: provide --tailnet or both --listen and --public-url")
		}
		cfg.Listen, cfg.PublicURL, cfg.AllowTailnetHTTP = *listen, *publicURL, *allowTailnetHTTP
	}
	if err := fleet.Create(path, cfg, fleet.LocalTailscaleIPs); err != nil {
		return fmt.Errorf("fleet init: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Created %s\n", path)
	return nil
}

func directTailnetFleetConfig(ips []netip.Addr) (fleet.Config, error) {
	var selected netip.Addr
	for _, ip := range ips {
		ip = ip.Unmap()
		if !ip.Is4() {
			continue
		}
		if selected.IsValid() && selected != ip {
			return fleet.Config{}, errors.New("more than one local Tailscale IPv4 address is active; use explicit --listen and --public-url")
		}
		selected = ip
	}
	if !selected.IsValid() {
		return fleet.Config{}, errors.New("no local Tailscale IPv4 address is active; use explicit --listen and --public-url")
	}
	address := net.JoinHostPort(selected.String(), "4346")
	return fleet.Config{
		Listen: address, PublicURL: "http://" + address, AllowTailnetHTTP: true,
		Local: fleet.Local{MaxConcurrent: 1, Contribution: "50%,50%"},
	}, nil
}

func runFleetAgentsEnroll(args []string) error {
	return runFleetAgentsEnrollTo(args, os.Stdout, os.Stderr)
}

func runFleetAgentsEnrollTo(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(cmdFleetAgentsEnroll.Path, flag.ContinueOnError)
	name := fs.String("name", "", "executor name")
	location := fs.String("location", "", "trusted placement (local|cloud)")
	capabilities := fs.StringSlice("capability", nil, "trusted capability (repeatable)")
	basePriority := fs.Int("base-priority", 50, "base scheduling priority (0-100)")
	priorityCeiling := fs.Int("priority-ceiling", 100, "highest effective priority (0-100)")
	maxConcurrent := fs.Int("max-concurrent", 1, "trusted concurrent slot ceiling")
	budgetCores := fs.Float64("budget-cores", 0, "trusted CPU contribution ceiling (0 = uncapped)")
	budgetMemory := fs.Int64("budget-memory-bytes", 0, "trusted memory contribution ceiling in bytes (0 = uncapped)")
	ttl := fs.Duration("ttl", 0, "credential lifetime (0 = never expires)")
	if err := parseAndCheck(cmdFleetAgentsEnroll, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *location != "local" && *location != "cloud" {
		return errors.New("fleet agents enroll: --location must be local or cloud")
	}
	if *ttl < 0 {
		return errors.New("fleet agents enroll: --ttl must be non-negative")
	}
	configPath := os.Getenv("SPARKWING_FLEET_CONFIG")
	var err error
	if configPath == "" {
		configPath, err = fleet.DefaultPath()
		if err != nil {
			return err
		}
	}
	_, err = fleet.Load(configPath, fleet.LocalTailscaleIPs)
	if err != nil {
		return fmt.Errorf("fleet agents enroll: load %s (create an owner-only fleet.yaml first): %w", configPath, err)
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return fmt.Errorf("fleet agents enroll: open local state: %w", err)
	}
	defer func() { _ = st.Close() }()
	caps := cleanFleetCapabilities(*capabilities)
	executor := store.Executor{
		Name: *name, Kind: "agent", Location: *location, Capabilities: caps,
		BasePriority: *basePriority, PriorityCeiling: *priorityCeiling,
		MaxConcurrent: *maxConcurrent,
		Budget:        store.ExecutorResource{Cores: *budgetCores, MemoryBytes: *budgetMemory},
	}
	entry := fleet.Executor{
		Name: *name, Location: *location,
		Capabilities: caps, BasePriority: *basePriority, PriorityCeiling: *priorityCeiling,
		MaxConcurrent: *maxConcurrent, Budget: executor.Budget,
	}
	_, err = fleet.AppendExecutorPrepared(configPath, entry, fleet.LocalTailscaleIPs, func(current fleet.Config) (func() error, error) {
		raw, tok, provisionErr := st.ProvisionExecutor(context.Background(), "fleet-executor:"+*name, executor, []string{
			controller.ScopeNodesClaim, controller.ScopeRunsState,
		}, *ttl, time.Now().UTC())
		if provisionErr != nil {
			return nil, provisionErr
		}
		rollback := func() error {
			return st.RollbackExecutorProvisioning(context.Background(), *name, tok.Prefix, time.Now().UTC())
		}
		snippet, encodeErr := fleetAgentSnippet(*name, current.PublicURL, raw)
		if encodeErr != nil {
			return rollback, fmt.Errorf("encode one-time agent config: %w", encodeErr)
		}
		if writeErr := writeAll(stdout, snippet); writeErr != nil {
			return rollback, fmt.Errorf("write one-time agent config: %w", writeErr)
		}
		return rollback, nil
	})
	if err != nil {
		return fmt.Errorf("fleet agents enroll: %w", err)
	}
	fmt.Fprintln(stderr, "WARNING: stash this token NOW. It is not recoverable after this command exits.")
	fmt.Fprintf(stderr, "Enrolled %s in %s. Atomically merge this one-time output into the helper's owner-only agent.yaml (0600 on Unix; protected user ACL on Windows):\n", *name, configPath)
	return nil
}

func fleetAgentSnippet(name, publicURL, raw string) ([]byte, error) {
	return yaml.Marshal(struct {
		Coordinators []struct {
			Name       string `yaml:"name"`
			Controller string `yaml:"controller"`
			Logs       string `yaml:"logs"`
			Token      string `yaml:"token"`
		} `yaml:"coordinators"`
	}{Coordinators: []struct {
		Name       string `yaml:"name"`
		Controller string `yaml:"controller"`
		Logs       string `yaml:"logs"`
		Token      string `yaml:"token"`
	}{{Name: name, Controller: publicURL, Logs: publicURL, Token: raw}}})
}

func writeAll(w io.Writer, body []byte) error {
	n, err := w.Write(body)
	if err != nil {
		return err
	}
	if n != len(body) {
		return io.ErrShortWrite
	}
	return nil
}

func cleanFleetCapabilities(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
