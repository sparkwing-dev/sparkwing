package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"golang.org/x/term"
)

func runProfiles(args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"profiles"}, args...))
	if err != nil {
		return err
	}
	format := inv.Output(term.IsTerminal(int(os.Stdout.Fd())))
	if inv.Help() || inv.Command.Path == "sparkwing profiles" {
		return writeCommandHelp(surfaceCommand(inv.Command.Path), os.Stdout, format)
	}
	switch inv.Command.Path {
	case "sparkwing profiles add":
		return addProfile(inv, format)
	case "sparkwing profiles remove":
		return removeProfile(inv, format)
	case "sparkwing profiles list":
		return listProfiles(format)
	case "sparkwing profiles show":
		p, err := resolveProfileFlag(inv.Value("profile"))
		if err != nil {
			return err
		}
		return writeProfile(p, format)
	case "sparkwing profiles set":
		return setProfile(inv, format)
	}
	return errors.New("profiles: subcommand required")
}

func loadCfg() (*profile.Config, string, error) {
	path, err := profile.DefaultPath()
	if err != nil {
		return nil, "", err
	}
	cfg, err := profile.Load(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}

func setProfile(inv clisurface.Invocation, format string) error {
	name := inv.Value("profile")
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[name]
	if !ok || p == nil {
		return fmt.Errorf("profiles set: %q not found", name)
	}
	if inv.Has("controller") {
		base, err := normalizeControllerURL(inv.Value("controller"))
		if err != nil {
			return err
		}
		if p.Controller == nil {
			p.Controller = &profile.ControllerSpec{}
		}
		p.Controller.URL = base
	}
	token := inv.Value("token")
	if inv.Bool("token-stdin") {
		token, err = readTokenStdin(os.Stdin, os.Stderr, fmt.Sprintf("new bearer token for %q", name))
		if err != nil {
			return err
		}
		if token == "" {
			return errors.New("profiles set: stdin carried no token")
		}
	}
	if inv.Has("token") || inv.Bool("token-stdin") {
		if p.Controller == nil {
			p.Controller = &profile.ControllerSpec{}
		}
		p.Controller.Token = token
	}
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	return writeProfile(p, format)
}

func readTokenStdin(in *os.File, prompt io.Writer, label string) (string, error) {
	if term.IsTerminal(int(in.Fd())) {
		fmt.Fprintf(prompt, "%s: ", label)
		raw, err := readPasswordRestoringEcho(int(in.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read token: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func readPasswordRestoringEcho(fd int) ([]byte, error) {
	state, err := term.GetState(fd)
	if err != nil {
		return term.ReadPassword(fd)
	}
	stop := restoreEchoOnSignal(func() { _ = term.Restore(fd, state) })
	defer stop()
	return term.ReadPassword(fd)
}

var exitOnSignal = os.Exit

func restoreEchoOnSignal(restore func()) func() {
	// safety: a terminal left in no-echo mode after an interrupt hides everything the user types next.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case s := <-sig:
			restore()
			signal.Stop(sig)
			code := 1
			if n, ok := s.(syscall.Signal); ok {
				code = 128 + int(n)
			}
			exitOnSignal(code)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}

func redactToken(t string) string {
	if t == "" {
		return "(none)"
	}
	if len(t) <= 12 {
		return "***"
	}
	return t[:4] + "..." + strings.Repeat("*", 8)
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
