package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

// safety: every gh call the connect path makes goes through one seam, so a
// test drives the command without a GitHub account or network.
var runGH = func(stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

// safety: how long the command waits for GitHub to deliver the ping it asked
// for; a test shortens both rather than waiting out a real delivery.
var (
	webhookPingAttempts = 15
	webhookPingInterval = 2 * time.Second
)

const defaultWebhookEvents = "push,pull_request"

func ghJSON(out any, stdin []byte, args ...string) error {
	raw, err := runGH(stdin, args...)
	if err != nil {
		return fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gh %s: decode response: %w", strings.Join(args, " "), err)
	}
	return nil
}

func newWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a webhook secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func splitEvents(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

type webhookHookPayload struct {
	Name   string            `json:"name,omitempty"`
	Active bool              `json:"active"`
	Events []string          `json:"events"`
	Config webhookHookConfig `json:"config"`
}

type webhookHookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Secret      string `json:"secret"`
	InsecureSSL string `json:"insecure_ssl"`
}

func runWebhooksConnect(args []string) error {
	fs := flag.NewFlagSet(cmdWebhooksConnect.Path, flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repo (OWNER/NAME)")
	pipeline := fs.String("pipeline", "", "pipeline the deliveries fire")
	eventsRaw := fs.String("events", defaultWebhookEvents, "GitHub events the webhook subscribes to")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdWebhooksConnect, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *repoFlag == "" {
		return errors.New("webhooks connect: --repo is required")
	}
	if *pipeline == "" {
		return errors.New("webhooks connect: --pipeline is required")
	}
	events := splitEvents(*eventsRaw)
	if len(events) == 0 {
		return errors.New("webhooks connect: --events names no event")
	}
	if *on == "" {
		return errors.New("webhooks connect: --profile is required (the controller stores the binding)")
	}
	if err := ghCLIAvailable(); err != nil {
		return fmt.Errorf("webhooks connect: %w", err)
	}
	repo, err := normalizeRepo(*repoFlag)
	if err != nil {
		return fmt.Errorf("webhooks connect: %w", err)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "webhooks connect"); err != nil {
		return err
	}
	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	secret, err := newWebhookSecret()
	if err != nil {
		return fmt.Errorf("webhooks connect: %w", err)
	}
	// safety: the binding is stored before the webhook exists, so the ping
	// GitHub sends the moment it is created already has a secret to verify.
	bound, err := c.ConnectGitHubWebhook(ctx, controller.GitHubWebhookBindingRequest{
		Pipeline: *pipeline, Repo: repo, Secret: secret, Events: events,
	})
	if err != nil {
		return fmt.Errorf("webhooks connect: store the binding: %w", err)
	}

	hookID, action, err := upsertGitHubHook(repo, bound.DeliveryURL, secret, events)
	if err != nil {
		return fmt.Errorf("webhooks connect: %w\n"+
			"the controller holds a binding for %s and github has no webhook for it; "+
			"run `sparkwing cluster webhooks disconnect --profile %s --repo %s --pipeline %s` "+
			"or connect again", err, repo, *on, repo, *pipeline)
	}
	if _, err := c.ConnectGitHubWebhook(ctx, controller.GitHubWebhookBindingRequest{
		Pipeline: *pipeline, Repo: repo, Secret: secret, Events: events, HookID: hookID,
	}); err != nil {
		return fmt.Errorf("webhooks connect: record the hook id: %w", err)
	}

	ping, pingErr := verifyGitHubPing(repo, hookID, time.Now().Add(-time.Second))

	fmt.Fprintf(os.Stdout, "connected %s to pipeline %s\n", repo, *pipeline)
	fmt.Fprintf(os.Stdout, "  delivery url: %s\n", bound.DeliveryURL)
	fmt.Fprintf(os.Stdout, "  hook:         %d (%s)\n", hookID, action)
	fmt.Fprintf(os.Stdout, "  events:       %s\n", strings.Join(events, ", "))
	fmt.Fprintln(os.Stdout, "  secret:       generated and stored on the controller")
	if pingErr != nil {
		fmt.Fprintf(os.Stdout, "  ping:         unverified: %v\n", pingErr)
		return fmt.Errorf("webhooks connect: the webhook is registered on both sides and the ping did not verify: %w", pingErr)
	}
	fmt.Fprintf(os.Stdout, "  ping:         the controller answered %d\n", ping)
	return nil
}

func upsertGitHubHook(repo, deliveryURL, secret string, events []string) (int64, string, error) {
	var hooks []githubHook
	if err := ghJSON(&hooks, nil, "api", "/repos/"+repo+"/hooks"); err != nil {
		return 0, "", err
	}
	payload := webhookHookPayload{
		Active: true,
		Events: events,
		Config: webhookHookConfig{
			URL: deliveryURL, ContentType: "json", Secret: secret, InsecureSSL: "0",
		},
	}
	for _, h := range hooks {
		if h.Config.URL != deliveryURL {
			continue
		}
		// safety: the secret travels on stdin, never in argv, which every
		// process on the machine can read.
		body, err := json.Marshal(payload)
		if err != nil {
			return 0, "", err
		}
		if err := ghJSON(nil, body, "api", "-X", "PATCH",
			fmt.Sprintf("/repos/%s/hooks/%d", repo, h.ID), "--input", "-"); err != nil {
			return 0, "", err
		}
		return h.ID, "updated", nil
	}
	payload.Name = "web"
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	var created githubHook
	if err := ghJSON(&created, body, "api", "-X", "POST",
		"/repos/"+repo+"/hooks", "--input", "-"); err != nil {
		return 0, "", err
	}
	if created.ID == 0 {
		return 0, "", errors.New("github created a webhook and reported no id")
	}
	return created.ID, "created", nil
}

// safety: a connect reports what the controller answered to a real delivery
// rather than assuming the URL and the secret are right.
func verifyGitHubPing(repo string, hookID int64, since time.Time) (int, error) {
	if err := ghJSON(nil, nil, "api", "-X", "POST",
		fmt.Sprintf("/repos/%s/hooks/%d/pings", repo, hookID)); err != nil {
		return 0, err
	}
	for attempt := range webhookPingAttempts {
		if attempt > 0 {
			time.Sleep(webhookPingInterval)
		}
		var deliveries []githubDelivery
		if err := ghJSON(&deliveries, nil, "api",
			fmt.Sprintf("/repos/%s/hooks/%d/deliveries?per_page=20", repo, hookID)); err != nil {
			return 0, err
		}
		for _, d := range deliveries {
			if d.Event != "ping" {
				continue
			}
			if ts, err := time.Parse(time.RFC3339, d.DeliveredAt); err == nil && ts.Before(since) {
				continue
			}
			if d.StatusCode >= 200 && d.StatusCode < 300 {
				return d.StatusCode, nil
			}
			return d.StatusCode, fmt.Errorf(
				"the controller answered %d (%s) to the ping", d.StatusCode, d.Status)
		}
	}
	return 0, errors.New("github recorded no ping delivery for the new webhook")
}

func runWebhooksDisconnect(args []string) error {
	fs := flag.NewFlagSet(cmdWebhooksDisconnect.Path, flag.ContinueOnError)
	repoFlag := fs.String("repo", "", "GitHub repo (OWNER/NAME)")
	pipeline := fs.String("pipeline", "", "pipeline the webhook fires")
	on := addProfileFlag(fs)
	if err := parseAndCheck(cmdWebhooksDisconnect, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if *repoFlag == "" {
		return errors.New("webhooks disconnect: --repo is required")
	}
	if *pipeline == "" {
		return errors.New("webhooks disconnect: --pipeline is required")
	}
	if *on == "" {
		return errors.New("webhooks disconnect: --profile is required (the controller holds the binding)")
	}
	if err := ghCLIAvailable(); err != nil {
		return fmt.Errorf("webhooks disconnect: %w", err)
	}
	repo, err := normalizeRepo(*repoFlag)
	if err != nil {
		return fmt.Errorf("webhooks disconnect: %w", err)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, "webhooks disconnect"); err != nil {
		return err
	}

	c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// safety: the controller answers with the webhook it was bound to, so the
	// delete names that webhook rather than every one aimed at the same
	// pipeline name on another controller.
	resp, err := c.DisconnectGitHubWebhook(ctx, *pipeline, repo)
	if err != nil {
		return fmt.Errorf("webhooks disconnect: remove the binding: %w", err)
	}

	var hooks []githubHook
	if err := ghJSON(&hooks, nil, "api", "/repos/"+repo+"/hooks"); err != nil {
		return fmt.Errorf("webhooks disconnect: %w", err)
	}
	deleted := []githubHook{}
	for _, h := range matchDisconnectHooks(hooks, *resp, *pipeline) {
		if err := ghJSON(nil, nil, "api", "-X", "DELETE",
			fmt.Sprintf("/repos/%s/hooks/%d", repo, h.ID)); err != nil {
			return fmt.Errorf("webhooks disconnect: %w", err)
		}
		deleted = append(deleted, h)
	}

	fmt.Fprintf(os.Stdout, "disconnected %s from pipeline %s\n", repo, *pipeline)
	if len(deleted) == 0 {
		fmt.Fprintln(os.Stdout, "  hook:    none pointed at that pipeline")
	}
	for _, h := range deleted {
		fmt.Fprintf(os.Stdout, "  hook:    deleted %d (%s)\n", h.ID, h.Config.URL)
	}
	if resp.Removed {
		fmt.Fprintln(os.Stdout, "  binding: removed from the controller")
	} else {
		fmt.Fprintln(os.Stdout, "  binding: the controller held none")
	}
	return nil
}

func matchDisconnectHooks(
	hooks []githubHook, resp controller.GitHubWebhookDisconnectResponse, pipeline string,
) []githubHook {
	exact := []githubHook{}
	for _, h := range hooks {
		if (resp.HookID != 0 && h.ID == resp.HookID) ||
			(resp.DeliveryURL != "" && h.Config.URL == resp.DeliveryURL) {
			exact = append(exact, h)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	// safety: a webhook written by hand, or against a URL this controller no
	// longer announces, is still this pipeline's; the caller sees which URL
	// each deleted hook carried.
	byPipeline := []githubHook{}
	for _, h := range hooks {
		if derivePipelineFromHookURL(h.Config.URL) == pipeline {
			byPipeline = append(byPipeline, h)
		}
	}
	return byPipeline
}
