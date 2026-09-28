package localws

import (
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

type originPolicy struct {
	allowRemote bool
	// safety: with a remote bind the request Host is attacker-controlled,
	// so the operator's own bind address anchors the Origin check instead.
	bindHost string
	// safety: another server on this machine shares the loopback host, so a
	// loopback origin is this dashboard's only on the port it serves or the
	// dashboard's own dev server.
	loopbackPorts []string
	allowOrigins  []string
}

// hack: pnpm dev in web/ serves the dashboard on this fixed port.
const devServerPort = "3100"

func originGuard(next http.Handler, policy originPolicy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// safety: the local API is unauthenticated, so a Host that is not
		// loopback means the request arrived through a name that resolves
		// here from someone else's network -- a DNS rebinding attempt.
		if !policy.allowRemote && !loopbackHost(r.Host) {
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

func (p originPolicy) originAllowed(origin string) bool {
	scheme, host, ok := splitOrigin(origin)
	if !ok {
		return false
	}
	// safety: `next dev` serves the dashboard on its own loopback port and
	// forwards that Origin; any other loopback port is another program.
	if loopbackHost(host) {
		return slices.Contains(p.loopbackPorts, originPort(scheme, host))
	}
	if p.bindHost != "" && strings.EqualFold(host, p.bindHost) {
		return true
	}
	for _, allowed := range p.allowOrigins {
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

// LoopbackBind reports whether addr binds a loopback interface. A bare
// host carrying no port is read as the host. Callers use it to decide
// whether serving the unauthenticated local API needs an explicit opt-in.
func LoopbackBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" {
		return false
	}
	return loopbackHost(host)
}

// safety: a wildcard bind names no interface, so it anchors no origin and
// must not widen the Origin check.
func bindOriginHost(addr string) string {
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
