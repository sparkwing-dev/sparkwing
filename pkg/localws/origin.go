package localws

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

func originGuard(next http.Handler, allowRemote bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// safety: the local API is unauthenticated, so a Host that is not
		// loopback means the request arrived through a name that resolves
		// here from someone else's network -- a DNS rebinding attempt.
		if !allowRemote && !loopbackHost(r.Host) {
			http.Error(w, "forbidden: this server answers loopback hosts only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !originAllowed(origin, r.Host) {
				http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// safety: a cross-site GET cannot read the response, but a
		// mutating one runs pipelines, so reject it when the browser
		// tells us the initiator was another site.
		if mutatingMethod(r.Method) && crossSiteFetch(r.Header.Get("Sec-Fetch-Site")) {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mutatingMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

func crossSiteFetch(secFetchSite string) bool {
	switch secFetchSite {
	case "", "same-origin", "none":
		return false
	}
	return true
}

func originAllowed(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if strings.EqualFold(u.Host, host) {
		return true
	}
	// safety: `next dev` serves the dashboard on another loopback port and
	// forwards its own Origin, so loopback origins stay allowed regardless
	// of port. Any origin off this machine is not.
	return loopbackHost(u.Host)
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	return loopbackHost(host)
}
