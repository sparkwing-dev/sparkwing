package controller_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controller"
)

func TestDevicePageRedirectsToConfiguredConsole(t *testing.T) {
	for _, dashboard := range []*controller.Dashboard{nil, {Bundle: pageBundle}} {
		f := newBrowserFixture(t, dashboard)
		f.srv.WithDashboardURL("https://console.example/")
		f.h = f.srv.Handler()
		for _, target := range []string{
			"https://api.example/device",
			"https://evil.example/device?next=https://evil.example&redirect_uri=//evil.example",
		} {
			r := httptest.NewRequest(http.MethodGet, target, nil)
			r.Header.Set("Forwarded", "host=evil.example;proto=http")
			r.Header.Set("X-Forwarded-Host", "evil.example")
			r.Header.Set("Origin", "https://evil.example")
			w := f.serve(r)
			if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "https://console.example/device" {
				t.Errorf("GET %s = %d %q", target, w.Code, w.Header().Get("Location"))
			}
		}
		w := f.serve(httptest.NewRequest(http.MethodGet, "https://console.example/device", nil))
		if strings.HasPrefix(w.Header().Get("Location"), "https://console.example/device") {
			t.Fatal("console redirects to itself")
		}
	}
}

func TestDevicePageWithoutConsoleKeepsSignIn(t *testing.T) {
	for _, console := range []string{"", ":bad", "//evil.example", "javascript:alert(1)"} {
		f := newBrowserFixture(t, &controller.Dashboard{Bundle: pageBundle})
		f.srv.WithDashboardURL(console)
		f.h = f.srv.Handler()
		w := f.get("/device")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login?next=%2Fdevice" {
			t.Errorf("console %q = %d %q", console, w.Code, w.Header().Get("Location"))
		}
	}
}
