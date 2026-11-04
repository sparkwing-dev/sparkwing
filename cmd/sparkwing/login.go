package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

var deviceLoginSleep = client.SleepContext

func runLogin(inv clisurface.Invocation) error {
	format := inv.Output(term.IsTerminal(int(os.Stdout.Fd())))
	if inv.Help() {
		return writeCommandHelp(surfaceCommand(inv.Command.Path), os.Stdout, format)
	}
	profiles, path, err := loadProfiles()
	if err != nil {
		return err
	}
	name := inv.Value("profile")
	existing, exists := profiles[name]
	var base string
	switch {
	case inv.Has("controller"):
		if base, err = normalizeControllerURL(inv.Value("controller")); err != nil {
			return err
		}
	case exists && existing.Controller.URL != "":
		base = existing.Controller.URL
	default:
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--controller is required unless --profile names a saved profile with a controller"}
	}
	if name == "" {
		name = profileNameForController(base)
		existing, exists = profiles[name]
	}
	if exists && existing.Controller.URL != base && !inv.Bool("force") {
		return fmt.Errorf("login: profile %q in %s points at %s; use --force to point it at %s", name, displayConfigPath(path), existing.Controller.URL, base)
	}
	machine := inv.Value("machine")
	if machine == "" {
		machine = deviceMachineName()
	}
	request := controllerwire.StartDeviceLoginRequest{Kind: controllerwire.DeviceLoginMember, Machine: machine, Grants: inv.Flags["scope"]}
	token, err := deviceSignIn(context.Background(), base, request)
	if err != nil {
		return err
	}
	p := &namedProfile{Name: name, Profile: userconfig.Profile{Controller: userconfig.Connection{URL: base, Token: &token.Capability}}}
	var replaced *namedProfile
	profiles = nil
	err = userconfig.Update(path, userconfig.Profiles, "profiles", &profiles, func(bool) error {
		if profiles == nil {
			profiles = make(map[string]userconfig.Profile)
		}
		if previous, ok := profiles[name]; ok && previous.Controller.Token != nil && *previous.Controller.Token != token.Capability {
			replaced = &namedProfile{Name: name, Profile: previous}
		}
		p.Default = profiles[name].Default
		profiles[name] = p.Profile
		return nil
	})
	if err != nil {
		return err
	}
	if replaced != nil && replaced.ControllerURL() == base {
		if revokeProfileToken(replaced) != nil {
			fmt.Fprintf(os.Stderr, "warning: the profile's previous token was not revoked; revoke it from the console's Machines page or with sparkwing tokens revoke --profile %s\n", name)
		}
	}
	fmt.Fprintf(os.Stderr, "Signed in to %s as %s with %s.\n", token.Team, token.Principal, strings.Join(token.Grants, ", "))
	return writeProfile(p, format)
}

func deviceSignIn(parent context.Context, base string, request controllerwire.StartDeviceLoginRequest) (controllerwire.CreatedToken, error) {
	// safety: an interrupt withdraws the request, so the code on screen stops working once the command exits.
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	login, err := client.NewDeviceLogin(client.EngineOptions{URL: base})
	if err != nil {
		return controllerwire.CreatedToken{}, err
	}
	started, err := login.Start(ctx, request)
	if err != nil {
		return controllerwire.CreatedToken{}, err
	}
	// safety: the page address comes from the URL the user chose, never from the response, so a reply cannot
	// send the user to approve somewhere else.
	if !strings.HasPrefix(started.VerificationPath, "/") || strings.HasPrefix(started.VerificationPath, "//") || strings.ContainsAny(started.VerificationPath, "?#\\") {
		return controllerwire.CreatedToken{}, &apierr.Error{Code: apierr.ProtocolError, Message: "the controller returned an unusable sign-in page path"}
	}
	code := started.UserCode
	if len(code) == 8 {
		code = code[:4] + "-" + code[4:]
	}
	fmt.Fprintf(os.Stderr, "To approve this sign-in, open %s%s in a browser where you are signed in, and enter the code:\n\n    %s\n\nWaiting for approval; the code expires in %d minutes. Press Ctrl-C to cancel.\n", base, started.VerificationPath, code, max(started.ExpiresIn/60, 1))
	token, err := login.Wait(ctx, started, deviceLoginSleep)
	if err != nil && ctx.Err() != nil {
		cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if cancelErr := login.Cancel(cancelCtx, started.DeviceCode); cancelErr != nil {
			return controllerwire.CreatedToken{}, errors.New("sign-in interrupted; the code stays valid until it expires because withdrawing it failed")
		}
		return controllerwire.CreatedToken{}, errors.New("sign-in cancelled; the code no longer works")
	}
	return token, err
}

func deviceMachineName() string {
	host, err := os.Hostname()
	if err != nil {
		return "cli"
	}
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-.")
	if len(name) > 63 {
		name = name[:63]
	}
	if name == "" {
		return "cli"
	}
	return name
}
