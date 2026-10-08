// Package originguard refuses browser requests that reach an unauthenticated
// loopback server from another site: a rebound DNS name, a cross-origin
// write, or a cross-site subresource. `sparkwing serve` wraps its handler in
// [Guard]; a controller serving the dashboard wraps its API in
// [RefuseCrossSiteWrites].
package originguard

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Policy says which hosts and browser origins a guarded server answers.
type Policy struct {
	// AllowRemote answers any Host. Without it a request must name a
	// loopback host or the host of an AllowOrigins entry.
	AllowRemote bool
	// BindHost is the server's own non-wildcard bind address; see
	// [BindOriginHost]. It anchors the Origin check under AllowRemote,
	// where the request Host is the caller's to choose.
	BindHost string
	// LoopbackPorts are the ports whose loopback origins count as this
	// server's own. Another server on this machine shares the loopback
	// host, so a dev server on another port is named in AllowOrigins.
	LoopbackPorts []string
	// AllowOrigins lists further browser origins ("https://dash.example"),
	// such as the public name of a proxy in front of the server or a
	// dashboard dev server on another loopback port.
	AllowOrigins []string
}

// NewPolicy builds the policy for a server bound to addr: the port it
// serves is its only loopback origin, and a non-wildcard bind anchors the
// Origin check.
func NewPolicy(addr string, allowRemote bool, allowOrigins []string) Policy {
	var ports []string
	if _, port, err := net.SplitHostPort(addr); err == nil {
		ports = append(ports, port)
	}
	return Policy{
		AllowRemote:   allowRemote,
		BindHost:      BindOriginHost(addr),
		LoopbackPorts: ports,
		AllowOrigins:  allowOrigins,
	}
}

// Guard answers 403 for a request from another site before it reaches next,
// and 415 for a browser write whose body is not JSON.
func Guard(next http.Handler, policy Policy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// safety: the guarded API is unauthenticated, so a Host that is
		// neither loopback nor configured means the request arrived through
		// a name that resolves here from someone else's network -- a DNS
		// rebinding attempt.
		if !policy.AllowRemote && !LoopbackHost(r.Host) && !policy.allowedHost(r.Host) {
			http.Error(w, "forbidden: this server answers loopback hosts only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !policy.originAllowed(origin) {
				http.Error(w, "forbidden: cross-origin request", http.StatusForbidden)
				return
			}
			if !jsonBodyOrNone(r) {
				http.Error(w, "unsupported media type: a browser write must send application/json", http.StatusUnsupportedMediaType)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// safety: a cross-site top-level navigation only renders the
		// dashboard, but a cross-site subresource -- img, script, fetch,
		// framed page -- sends no Origin and exists to reach a side effect,
		// so it is refused whatever the method.
		if crossSiteFetch(r.Header.Get("Sec-Fetch-Site")) &&
			(mutatingMethod(r.Method) || r.Header.Get("Sec-Fetch-Dest") != "document") {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RefuseCrossSiteWrites answers 403 for a write a browser sent from another
// site: an Origin whose host is not the request's Host, or a Sec-Fetch-Site
// of cross-site or same-site. It serves a listener reachable under names
// nobody configured, where [Guard] would refuse its own users. A request
// carrying neither header is not a browser's and passes, because the attack
// needs a victim's browser to send it.
func RefuseCrossSiteWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !mutatingMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameHost(origin, r.Host) {
			http.Error(w, "forbidden: cross-site write: Origin "+origin+" is not this dashboard's host "+r.Host, http.StatusForbidden)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" || site == "same-site" {
			http.Error(w, "forbidden: cross-site write: Sec-Fetch-Site is "+site, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hack: a proxy may spell the scheme's default port in Host while the browser's Origin omits it.
func sameHost(origin, host string) bool {
	scheme, originHost, ok := splitOrigin(origin)
	if !ok {
		return false
	}
	def := ":" + originPort(scheme, "")
	return strings.EqualFold(strings.TrimSuffix(originHost, def), strings.TrimSuffix(host, def))
}

// safety: a form or text/plain POST is a simple request a page can send
// without a preflight, so a browser write that carries a body must name
// JSON, which no page can send cross-origin without one.
func jsonBodyOrNone(r *http.Request) bool {
	if !mutatingMethod(r.Method) {
		return true
	}
	contentType := r.Header.Get("Content-Type")
	if contentType == "" && r.ContentLength == 0 {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "application/json"
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

func (p Policy) allowedHost(host string) bool {
	for _, allowed := range p.AllowOrigins {
		if _, allowedHost, ok := splitOrigin(allowed); ok && strings.EqualFold(allowedHost, host) {
			return true
		}
	}
	return false
}

func (p Policy) originAllowed(origin string) bool {
	scheme, host, ok := splitOrigin(origin)
	if !ok {
		return false
	}
	// safety: another loopback port is another program, which may belong to
	// another account on this machine.
	if LoopbackHost(host) && slices.Contains(p.LoopbackPorts, originPort(scheme, host)) {
		return true
	}
	if !LoopbackHost(host) && p.BindHost != "" && strings.EqualFold(host, p.BindHost) {
		return true
	}
	for _, allowed := range p.AllowOrigins {
		allowedScheme, allowedHost, allowedOK := splitOrigin(allowed)
		if allowedOK && allowedScheme == scheme && strings.EqualFold(allowedHost, host) {
			return true
		}
	}
	return false
}

func originPort(scheme, host string) string {
	if _, port, err := net.SplitHostPort(host); err == nil {
		return port
	}
	if scheme == "https" {
		return "443"
	}
	return "80"
}

func splitOrigin(origin string) (scheme, host string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u.Host == "" {
		return "", "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", false
	}
	return u.Scheme, u.Host, true
}

// LoopbackHost reports whether hostport names localhost or a loopback IP.
func LoopbackHost(hostport string) bool {
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

// BindOriginHost returns addr as an origin host, or "" for a wildcard bind,
// which names no interface and so must not widen the Origin check.
func BindOriginHost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return ""
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return ""
	}
	return net.JoinHostPort(host, port)
}
