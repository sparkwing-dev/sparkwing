package main

import (
	"context"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func addProfile(inv clisurface.Invocation, format string) error {
	if inv.Has("scope") {
		return errors.New("profiles add: credential enrollment with --scope is unavailable; use --token-stdin with an existing credential")
	}
	base, err := normalizeControllerURL(inv.Value("controller"))
	if err != nil {
		return err
	}
	name := inv.Value("profile")
	if name == "" {
		name = profileNameForController(base)
	}
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	if _, exists := cfg.Profiles[name]; exists && !inv.Bool("force") {
		return fmt.Errorf("profiles add: profile %q already exists in %s; use --force to replace it", name, displayConfigPath(path))
	}
	p := &profile.Profile{Name: name, Controller: &profile.ControllerSpec{URL: base}}
	if inv.Bool("token-stdin") {
		p.Controller.Token, err = readTokenStdin(os.Stdin, os.Stderr, fmt.Sprintf("bearer token for %q", name))
		if err != nil {
			return err
		}
		if p.Controller.Token == "" {
			return errors.New("profiles add: stdin carried no token")
		}
	}
	if !inv.Bool("no-probe") {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := checkProfileConnection(ctx, p); err != nil {
			return err
		}
	}
	cfg.Profiles[name] = p
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	return writeProfile(p, format)
}

func checkProfileConnection(ctx context.Context, p *profile.Profile) error {
	if reach := probeController(ctx, p); reach.Status == "fail" {
		return fmt.Errorf("profiles add: controller probe failed: %s", reach.Detail)
	}
	if p.ControllerToken() == "" {
		return errors.New("profiles add: --token-stdin is required to verify controller access; use --no-probe to save without verification")
	}
	if auth := probeAuth(ctx, p); auth.Status == "fail" {
		return fmt.Errorf("profiles add: authentication failed: %s", auth.Detail)
	}
	return nil
}

func removeProfile(inv clisurface.Invocation, format string) error {
	name := inv.Value("profile")
	cfg, path, err := loadCfg()
	if err != nil {
		return err
	}
	p, ok := cfg.Profiles[name]
	if !ok || p == nil {
		return fmt.Errorf("profiles remove: %q not found", name)
	}
	if !inv.Bool("keep-token") {
		if err := revokeProfileToken(p); err != nil {
			return err
		}
	}
	delete(cfg.Profiles, name)
	if err := profile.Save(path, cfg); err != nil {
		return err
	}
	return writeProfile(p, format)
}

func revokeProfileToken(p *profile.Profile) error {
	raw := p.ControllerToken()
	if raw == "" {
		return nil
	}
	prefix, err := tokenPrefix(raw)
	if err != nil {
		return fmt.Errorf("profiles remove: %w; use --keep-token to remove only the connection", err)
	}
	ctx := context.Background()
	c, err := client.NewTokenManager(client.EngineOptions{URL: p.ControllerURL(), Bearer: func(context.Context) (string, error) { return raw, nil }}, false)
	if err != nil {
		return err
	}
	found, err := c.ReadToken(ctx, prefix)
	if err != nil {
		return fmt.Errorf("profiles remove: cannot look up token %s: %w; use --keep-token to remove only the connection", prefix, err)
	}
	// safety: removing a connection must not revoke a runner's machine credential.
	if found.Kind != "member" {
		return errors.New("profiles remove: the connection holds a machine credential; use --keep-token to remove only the connection")
	}
	if _, err := c.RevokeToken(ctx, prefix); err != nil {
		return fmt.Errorf("profiles remove: cannot revoke token %s: %w; use --keep-token to remove only the connection", prefix, err)
	}
	return nil
}

func normalizeControllerURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := neturl.Parse(trimmed)
	if err != nil {
		return "", errors.New("profiles: --controller must be an http or https URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("profiles: --controller must be an http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("profiles: --controller must not contain credentials, query parameters or a fragment")
	}
	return trimmed, nil
}

func profileNameForController(controllerURL string) string {
	u, err := neturl.Parse(controllerURL)
	if err != nil {
		return "cloud"
	}
	var b strings.Builder
	for _, r := range strings.ToLower(u.Hostname()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		return "cloud"
	}
	return name
}
