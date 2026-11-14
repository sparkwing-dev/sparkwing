package controller

import "testing"

func TestLocalSubscriptionsAcceptLoopbackReceivers(t *testing.T) {
	for _, url := range []string{"http://localhost:9000/hook", "http://127.0.0.1:9000/hook", "https://[::1]/hook", "http://hooks.example.com/a"} {
		if _, err := validSubscriptionURL(url, true); err != nil {
			t.Errorf("local %q: %v", url, err)
		}
		if _, err := validSubscriptionURL(url, false); err == nil {
			t.Errorf("controller accepted %q", url)
		}
	}
	for _, url := range []string{"ftp://localhost/a", "http://user@localhost/a", "http://localhost/a#x", "localhost:9000"} {
		if _, err := validSubscriptionURL(url, true); err == nil {
			t.Errorf("local accepted %q", url)
		}
	}
}

func TestSigningSecretsAreStandardWebhookKeys(t *testing.T) {
	a, err := newSigningSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newSigningSecret()
	if len(a) != len("whsec_")+44 || a[:6] != "whsec_" || a == b {
		t.Fatalf("secrets %q %q", a, b)
	}
}
