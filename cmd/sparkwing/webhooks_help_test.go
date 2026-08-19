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
