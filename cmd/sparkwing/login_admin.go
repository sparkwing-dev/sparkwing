package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/opsview"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

// safety: the request travels with the profile's member token, so only the same account can approve it.
func loginAdmin(inv clisurface.Invocation, console deviceConsole, format string) error {
	if inv.Has("scope") || inv.Bool("force") {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--admin uses the profile's current sign-in; drop --scope and --force"}
	}
	profiles, path, err := loadProfiles()
	if err != nil {
		return err
	}
	name := inv.Value("profile")
	if inv.Has("controller") {
		base, err := normalizeControllerURL(inv.Value("controller"))
		if err != nil {
			return err
		}
		if name == "" {
			name = profileNameForController(base)
		}
		if existing, ok := profiles[name]; ok && existing.Controller.URL != base {
			return &clisurface.UsageError{Path: inv.Command.Path, Message: fmt.Sprintf("--admin keeps profile %q on %s; drop --controller", name, existing.Controller.URL)}
		}
	}
	existing, ok := profiles[name]
	if !ok || existing.Controller.Token == nil || *existing.Controller.Token == "" {
		return &clisurface.UsageError{Path: inv.Command.Path, Message: "--admin steps up a signed-in profile; run sparkwing login first, then sparkwing login --admin"}
	}
	previous := &namedProfile{Name: name, Profile: existing}
	base, member := existing.Controller.URL, *existing.Controller.Token
	machine := inv.Value("machine")
	if machine == "" {
		machine = deviceMachineName()
	}
	stepUp, err := client.NewDeviceStepUp(client.EngineOptions{URL: base, Bearer: func(context.Context) (string, error) { return member, nil }})
	if err != nil {
		return err
	}
	approved, err := deviceApproval(context.Background(), console, base, func(ctx context.Context, _ *client.DeviceLogin) (controllerwire.DeviceLoginStarted, error) {
		return stepUp.Start(ctx, machine)
	})
	if err != nil {
		return err
	}
	admin, renewed := *approved.Credential, approved.Member
	if renewed == nil || renewed.Team != admin.Team || admin.ExpiresAt == nil {
		revokeUnsaved(base, approved.Credential, renewed)
		return &apierr.Error{Code: apierr.ProtocolError, Message: "the controller approved administration without a member token and an expiry; nothing was saved"}
	}
	if err := checkApprovedTeam(admin, inv.Value("team")); err != nil {
		revokeUnsaved(base, approved.Credential, renewed)
		return err
	}
	p := &namedProfile{Name: name}
	profiles = nil
	err = userconfig.Update(path, userconfig.Profiles, "profiles", &profiles, func(bool) error {
		current, ok := profiles[name]
		if !ok || current.Controller.URL != base || !sameProfileToken(current.Controller.Token, existing.Controller.Token) {
			return fmt.Errorf("login --admin: profile %q changed while waiting for approval; run sparkwing login --admin again", name)
		}
		current.Controller.Token = &renewed.Capability
		current.Controller.AdminToken, current.Controller.AdminExpiresAt = &admin.Capability, admin.ExpiresAt
		profiles[name], p.Profile = current, current
		return nil
	})
	if err != nil {
		revokeUnsaved(base, approved.Credential, renewed)
		return err
	}
	if err := revokeProfileToken(previous); err != nil && !credentialEnded(err) {
		fmt.Fprintf(os.Stderr, "warning: the profile's previous tokens were not revoked; revoke them from the console's Machines page or with sparkwing tokens revoke --profile %s\n", opsview.DisplayText(name))
	}
	fmt.Fprintf(os.Stderr, "This profile holds team.admin until %s, in about %d minutes. After that it uses its member token; run sparkwing login --admin again when you need administration.\n",
		admin.ExpiresAt.Local().Format("15:04 MST"), int(math.Round(time.Until(*admin.ExpiresAt).Minutes())))
	return writeProfile(p, format)
}

func revokeUnsaved(base string, tokens ...*controllerwire.CreatedToken) {
	for _, token := range tokens {
		if token == nil {
			continue
		}
		if err := revokeToken(base, token.Capability); err != nil && !credentialEnded(err) {
			fmt.Fprintf(os.Stderr, "warning: token %s is still live; revoke it from the console\n", opsview.DisplayText(token.Prefix))
		}
	}
}

func credentialEnded(err error) bool {
	var failure *apierr.Error
	return errors.As(err, &failure) && (failure.Code == hosterr.Unauthenticated || failure.Code == apierr.NotFound)
}
