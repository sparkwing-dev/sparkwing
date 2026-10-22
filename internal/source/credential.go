package source

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/fssecure"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/source/gitcmd"
	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
)

func credentialRemote(remote string, credential Credential) (string, error) {
	host, err := sourceurl.Host(remote)
	if err != nil {
		return "", err
	}
	if credential.Kind == "" {
		if credential.Secret != "" || credential.Username != "" || credential.KnownHosts != "" {
			return "", errors.New("source credential material requires an explicit kind")
		}
		return remote, nil
	}
	if !strings.EqualFold(host, credential.Host) {
		return "", errors.New("source credential belongs to another host")
	}
	switch credential.Kind {
	case "github_app", "https":
		if credential.Kind == "github_app" && host != "github.com" {
			return "", errors.New("GitHub App credential requires github.com")
		}
		if credential.Secret == "" || strings.ContainsAny(credential.Secret+credential.Username, "\x00\r\n") || strings.Contains(credential.Username, ":") {
			return "", errors.New("source HTTPS credential is malformed")
		}
		if credential.Kind == "https" && credential.Username == "" {
			return "", errors.New("source HTTPS credential requires a username")
		}
		return sourceurl.HTTPSForm(remote)
	case "ssh":
		if !strings.Contains(credential.Secret, "PRIVATE KEY") || credential.KnownHosts == "" || strings.ContainsAny(credential.KnownHosts, "\x00\r") {
			return "", errors.New("source SSH credential requires a private key and confirmed host keys")
		}
		return sourceurl.SSHForm(remote)
	default:
		return "", errors.New("source credential kind is unsupported")
	}
}

func (g *gitClient) authentication(ctx context.Context, remote string) (args, env []string, cleanup func() error, err error) {
	cleanup = func() error { return nil }
	credential := g.credential
	if credential.Kind == "" && g.owner {
		credential, err = g.ownerHTTPSCredential(ctx, remote)
		if err != nil {
			return nil, nil, cleanup, err
		}
	}
	if credential.Kind == "" {
		command := "ssh -o BatchMode=yes -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes"
		if !g.owner {
			command = "ssh -F " + shellQuote(os.DevNull) + " -o BatchMode=yes -o IdentitiesOnly=yes -o IdentityAgent=none -o IdentityFile=none -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes"
		}
		env = []string{"GIT_SSH_COMMAND=" + command}
		return nil, env, cleanup, nil
	}
	directory, err := fssecure.MkdirPrivateTemp(g.workspace, "credential-")
	if err != nil {
		return nil, nil, cleanup, err
	}
	cleanup = func() error { return os.RemoveAll(directory) }
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup())
		}
	}()
	if credential.Kind == "ssh" {
		key, known := filepath.Join(directory, "key"), filepath.Join(directory, "known_hosts")
		if err := fssecure.WriteFile(key, []byte(credential.Secret)); err != nil {
			return nil, nil, cleanup, err
		}
		if err := fssecure.WriteFile(known, []byte(credential.KnownHosts)); err != nil {
			return nil, nil, cleanup, err
		}
		command := "ssh -F " + shellQuote(os.DevNull) + " -o BatchMode=yes -o IdentitiesOnly=yes -o IdentityAgent=none -o StrictHostKeyChecking=yes -o ForwardAgent=no -o ClearAllForwardings=yes -o GlobalKnownHostsFile=" + shellQuote(os.DevNull) + " -o UserKnownHostsFile=" + shellQuote(known) + " -i " + shellQuote(key)
		return nil, []string{"GIT_SSH_COMMAND=" + command, "HOME=" + directory}, cleanup, nil
	}
	username := credential.Username
	if credential.Kind == "github_app" {
		username = "x-access-token"
	}
	header := "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+credential.Secret))
	config := filepath.Join(directory, "gitconfig")
	body := "[http " + strconv.Quote(strings.TrimRight(remote, "/")) + "]\n\textraHeader = " + strconv.Quote(header) + "\n"
	if err := fssecure.WriteFile(config, []byte(body)); err != nil {
		return nil, nil, cleanup, err
	}
	return []string{"-c", "include.path=" + config}, []string{"HOME=" + directory}, cleanup, nil
}

func (g *gitClient) ownerHTTPSCredential(ctx context.Context, remote string) (Credential, error) {
	if !strings.HasPrefix(remote, "https://") {
		return Credential{}, nil
	}
	var ownerEnvironment []string
	for _, item := range g.env {
		if !strings.HasPrefix(item, "GIT_CONFIG_GLOBAL=") && !strings.HasPrefix(item, "GIT_CONFIG_NOSYSTEM=") {
			ownerEnvironment = append(ownerEnvironment, item)
		}
	}
	probe := procenv.ControlCommandContext(ctx, ownerEnvironment, "git", "config", "--get-urlmatch", "credential.helper", remote)
	probe.Dir = g.workspace
	helper, err := gitcmd.Output(ctx, probe, false)
	if gitExit(err, 1) || err == nil && strings.TrimSpace(string(helper)) == "" {
		return Credential{}, nil
	}
	if err != nil {
		return Credential{}, errors.New("read owner source credential configuration failed")
	}
	cmd := procenv.ControlCommandContext(ctx, ownerEnvironment, "git", "credential", "fill")
	cmd.Dir = g.workspace
	cmd.Stdin = strings.NewReader("url=" + remote + "\n\n")
	out, err := gitcmd.Output(ctx, cmd, false)
	if err != nil {
		return Credential{}, errors.New("owner source credential helper failed")
	}
	credential := Credential{Kind: "https"}
	credential.Host, _ = sourceurl.Host(remote)
	for _, line := range strings.Split(string(out), "\n") {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "username":
			credential.Username = value
		case "password":
			credential.Secret = value
		}
	}
	if _, err := credentialRemote(remote, credential); err != nil {
		return Credential{}, errors.New("owner source credential helper returned unusable credentials")
	}
	return credential, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(filepath.ToSlash(value), "'", "'\\''") + "'"
}

func authorizedSubmodule(parent, remote string, credential Credential) error {
	identity, err := sourceurl.CaseIdentity(remote)
	if err != nil {
		return err
	}
	parentIdentity, err := sourceurl.CaseIdentity(parent)
	if err != nil {
		return err
	}
	host, _, _ := strings.Cut(parentIdentity, "/")
	for _, repository := range credential.ExtraRepositories {
		allowed, err := sourceurl.CaseIdentity("https://" + host + "/" + repository)
		if err == nil && identity == allowed {
			return nil
		}
	}
	return fmt.Errorf("source submodule %q is outside the authorized repository list", identity)
}
