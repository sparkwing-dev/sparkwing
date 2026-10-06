package controller_test

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

// The mux unescapes each path segment before matching, so a literal such as
// "runs" can arrive spelled %72uns and still reach a run handler. Every run
// and trigger route answers another team's run with the boundary's 404 under
// those spellings too.
func TestTeamBoundary_EncodedPathSegmentsStillAnswerAnotherTeamWith404(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		spellings := []struct{ name, from, to string }{
			{"escaped collection", "/runs/", "/%72uns/"},
			{"escaped trigger collection", "/triggers/", "/%74riggers/"},
			{"escaped api segment", "/api/", "/%61pi/"},
			{"escaped id byte", f.runA, strings.Replace(f.runA, "-", "%2D", 1)},
		}
		checked := 0
		for pattern := range controller.MuxRouteScopes(t) {
			if !runScoped(pattern) {
				continue
			}
			if _, exempt := controller.TeamBoundaryExempt[pattern]; exempt {
				continue
			}
			method, path := runRoutePath(pattern, f.runA)
			for _, sp := range spellings {
				variant := strings.Replace(path, sp.from, sp.to, 1)
				if variant == path {
					continue
				}
				checked++
				code, body := f.do(method, variant, f.everyScopeB, map[string]any{})
				if code != http.StatusNotFound || strings.Contains(body, controller.UnsupportedRouteError) {
					t.Errorf("%s %s (%s) as team B = %d want the boundary's 404: %s", method, variant, sp.name, code, body)
				}
			}
		}
		if checked < 120 {
			t.Errorf("checked %d spellings; server.go registers far more run routes, so the enumeration is broken", checked)
		}
		if code, body := f.do("GET", "/api/v1/%72uns/"+f.runA, f.ownerA, nil); code != http.StatusOK {
			t.Errorf("team A cannot read its own run through an escaped segment: %d %s", code, body)
		}
		run, err := f.teamA.GetRun(context.Background(), f.runA)
		if err != nil || run.Status != "running" {
			t.Errorf("team B's requests changed team A's run: %+v, %v", run, err)
		}
		if n := cancelRequests(t, f.st); n != 0 {
			t.Errorf("team B's requests asked %d triggers to cancel", n)
		}
	})
}

// Paths the router cleans are redirected before the boundary or any handler
// sees them; paths the mux refuses or never matches answer another team with
// a 404, a 405 or a 400, and never with the run.
func TestTeamBoundary_NonCanonicalPathsNeverServeAnotherTeamsRun(t *testing.T) {
	tenancyDialects(t, func(t *testing.T, f *tenancyFixture) {
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		run := f.runA
		cases := []struct {
			method, path string
			want         []int
		}{
			{"GET", "/api/v1/runs//" + run, []int{http.StatusTemporaryRedirect}},
			{"GET", "/api/v1/runs/./" + run, []int{http.StatusTemporaryRedirect}},
			{"GET", "/api/v1/runs/x/../" + run, []int{http.StatusTemporaryRedirect}},
			{"GET", "/api/v1//runs/" + run, []int{http.StatusTemporaryRedirect}},
			{"GET", "/api/v1/runs/" + run + "/", []int{http.StatusNotFound}},
			{"GET", "/api/v1/runs/%2E%2E/runs/" + run, []int{http.StatusNotFound}},
			{"GET", "/api/v1/runs/" + run + "%2Fnodes", []int{http.StatusNotFound}},
			{"GET", "/api/v1/runs/" + strings.Replace(run, "-", "%252D", 1), []int{http.StatusNotFound}},
			{"GET", "/api/v1/runs/" + strings.Replace(run, "a", "%EF%BD%81", 1), []int{http.StatusNotFound}},
			{"GET", "/api/v1/triggers//" + run, []int{http.StatusTemporaryRedirect}},
			{"GET", "/api/v1/triggers/" + run + "%2F", []int{http.StatusNotFound}},
			{"PATCH", "/api/v1/runs/" + run, []int{http.StatusMethodNotAllowed}},
			{"PATCH", "/api/v1/%72uns/" + run, []int{http.StatusMethodNotAllowed}},
			{"POST", "/api/v1/runs/./" + run + "/cancel", []int{http.StatusTemporaryRedirect}},
		}
		for _, c := range cases {
			req, err := http.NewRequest(c.method, f.url+c.path, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", f.everyScopeB)
			resp, err := noFollow.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", c.method, c.path, err)
			}
			body := readAll(t, resp)
			_ = resp.Body.Close()
			if !containsCode(c.want, resp.StatusCode) || strings.Contains(body, "build-a") {
				t.Errorf("%s %s as team B = %d want one of %v: %s", c.method, c.path, resp.StatusCode, c.want, body)
			}
			if resp.StatusCode == http.StatusTemporaryRedirect {
				loc, err := url.Parse(resp.Header.Get("Location"))
				if err != nil || strings.Contains(loc.Path, "//") || strings.Contains(loc.Path, "/.") {
					t.Errorf("%s %s redirected to an uncleaned %q", c.method, c.path, resp.Header.Get("Location"))
				}
			}
		}
		run2, err := f.teamA.GetRun(context.Background(), run)
		if err != nil || run2.Status != "running" {
			t.Errorf("team B's requests changed team A's run: %+v, %v", run2, err)
		}
		if n := cancelRequests(t, f.st); n != 0 {
			t.Errorf("team B's requests asked %d triggers to cancel", n)
		}

		u, err := url.Parse(f.url)
		if err != nil {
			t.Fatal(err)
		}
		conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := conn.Write([]byte("GET /api/v1/runs/" + run + "%zz HTTP/1.1\r\nHost: " + u.Host +
			"\r\nAuthorization: " + f.everyScopeB + "\r\nConnection: close\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if body := readAll(t, resp); resp.StatusCode != http.StatusBadRequest || strings.Contains(body, "build-a") {
			t.Errorf("an invalid escape as team B = %d want the server's 400: %s", resp.StatusCode, body)
		}
	})
}

func containsCode(codes []int, code int) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	var b strings.Builder
	if _, err := bufio.NewReader(resp.Body).WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
