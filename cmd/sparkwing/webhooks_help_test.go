package main

import (
	"strings"
	"testing"
)

func TestWebhookHelpUsesProfilesOnlyForControllerLookups(t *testing.T) {
	for _, command := range []Command{cmdWebhooksList, cmdWebhooksReplay} {
		for _, flag := range command.Flags {
			if flag.Name == "profile" {
				t.Errorf("%s advertises unused --profile", command.Path)
			}
		}
	}

	foundDeliveriesProfile := false
	for _, flag := range cmdWebhooksDeliveries.Flags {
		if flag.Name == "profile" {
			foundDeliveriesProfile = true
			if !flag.Required {
				t.Error("webhook deliveries does not require its controller profile")
			}
		}
	}
	if !foundDeliveriesProfile {
		t.Fatal("webhook deliveries does not declare --profile")
	}

	for _, command := range allCommands {
		for _, example := range command.Examples {
			if !strings.Contains(example.Command, "sparkwing cluster webhooks list") &&
				!strings.Contains(example.Command, "sparkwing cluster webhooks replay") {
				continue
			}
			if strings.Contains(example.Command, "--profile") {
				t.Errorf("%s example advertises unused webhook --profile: %q", command.Path, example.Command)
			}
		}
	}
}

func TestWebhookListAndReplayRejectProfile(t *testing.T) {
	commands := []struct {
		name string
		run  func([]string) error
	}{
		{name: "list", run: runWebhooksList},
		{name: "replay", run: runWebhooksReplay},
	}

	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			var err error
			captureStderr(t, func() {
				err = command.run([]string{"--profile", "prod"})
			})
			if err == nil || !strings.Contains(err.Error(), "unknown flag: --profile") {
				t.Fatalf("error = %v, want unknown --profile flag", err)
			}
		})
	}
}
