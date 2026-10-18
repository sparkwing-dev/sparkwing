package wingd

import (
	"context"
	"net"
	"net/http"
	"testing"
)

func TestLocalDashboardRefusesForeignHostsAndOrigins(t *testing.T) {
	f := &runtimeControllerFixture{}
	f.http = func() LocalHTTP {
		ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		return LocalHTTP{API: ok, Dashboard: ok}
	}
	r := newRuntimeFixture(t, f)
	bound := make(chan string, 1)
	r.listen = func(network, address string) (net.Listener, error) {
		if network != "tcp" {
			return &runtimeListener{closed: make(chan struct{})}, nil
		}
		ln, err := net.Listen(network, address)
		if err == nil {
			bound <- ln.Addr().String()
		}
		return ln, err
	}
	lifecycle := NewLocalLifecycle(r, nil)
	opts := LocalStartOptions{Home: f.home, Version: "v1", Stay: true}
	if _, err := lifecycle.EnsureStarted(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	opts.DashboardAddress = "127.0.0.1:0"
	if _, err := lifecycle.StartDashboard(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	addr := <-bound
	for _, tc := range []struct {
		host, origin string
		want         int
	}{
		{addr, "", http.StatusOK},
		{"localhost", "http://" + addr, http.StatusOK},
		{"rebound.example", "", http.StatusForbidden},
		{addr, "http://evil.example", http.StatusForbidden},
		{addr, "http://127.0.0.1:1", http.StatusForbidden},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/api/v1/runs", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = tc.host
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Errorf("Host %q Origin %q = %d, want %d", tc.host, tc.origin, resp.StatusCode, tc.want)
		}
	}
}
