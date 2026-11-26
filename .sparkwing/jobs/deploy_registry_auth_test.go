package jobs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func foreignRegistry(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var authed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			authed.Add(1)
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="foreign"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://"), &authed
}

func TestECRCredentialNeverReachesAForeignPreviousPin(t *testing.T) {
	host, authed := foreignRegistry(t)
	values := strings.ReplaceAll(testValues("v0.68.3", testDigest("old-ctl"), testDigest("old-run")), testRegistry, host)
	pins, err := readValuesPins(values)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pins.Controller.Repository, host+"/") {
		t.Fatalf("fixture pin %s is not on the foreign host", pins.Controller.Repository)
	}
	err = (ecrRegistry{host: testRegistry}).retain(t.Context(), "ecr-password", pins.Controller)
	if n := authed.Load(); n != 0 {
		t.Fatalf("the ECR credential reached %s %d times", host, n)
	}
	if err == nil || !strings.Contains(err.Error(), "refusing to send the ECR credential to "+host) {
		t.Fatalf("retain of a foreign previous pin = %v", err)
	}
}

func TestECRCredentialNeverReachesAForeignBaseOverride(t *testing.T) {
	host, authed := foreignRegistry(t)
	f := newFakeDeploy(t)
	d := f.deployer(false)
	d.tools.registry = ecrRegistry{}
	d.cfg.ControllerBase = host + "/sparkwing-controller@" + testDigest("foreign-base")
	if err := d.signIn(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := d.tools.registry.(ecrRegistry).host; got != testRegistry {
		t.Fatalf("sign-in bound the credential to %q, want %q", got, testRegistry)
	}
	_, err := d.tools.registry.digest(t.Context(), "ecr-password", d.cfg.ControllerBase)
	if n := authed.Load(); n != 0 {
		t.Fatalf("the ECR credential reached %s %d times", host, n)
	}
	if err == nil || !strings.Contains(err.Error(), "refusing to send the ECR credential to "+host) {
		t.Fatalf("digest of a foreign --controller-base = %v", err)
	}
}

func TestECRCredentialIsRefusedBeforeSignIn(t *testing.T) {
	host, authed := foreignRegistry(t)
	_, err := (ecrRegistry{}).digest(t.Context(), "ecr-password", host+"/sparkwing-runner:latest")
	if n := authed.Load(); n != 0 {
		t.Fatalf("the ECR credential reached %s %d times", host, n)
	}
	if err == nil || !strings.Contains(err.Error(), `the deploy registry is ""`) {
		t.Fatalf("an unbound registry = %v", err)
	}
}
