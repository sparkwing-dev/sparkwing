package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/fleet"
)

func runFleet(args []string) error {
	if handleParentHelp(cmdFleet, args) {
		return nil
	}
	if len(args) == 0 {
		PrintHelp(cmdFleet, os.Stdout)
		return nil
	}
	if args[0] != "init" {
		return fmt.Errorf("fleet: unknown subcommand %q", args[0])
	}
	return runFleetInit(args[1:])
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
	path := os.Getenv(fleet.PathEnv)
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
