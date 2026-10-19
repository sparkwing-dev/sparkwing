package cluster

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// safety: a runner and the launcher send their machine token with every
// request, so plain http is allowed only where the traffic cannot leave the
// host or the cluster network: loopback, or a Kubernetes Service name.
func checkControllerTransport(flag, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s %q is not an absolute URL", flag, raw)
	}
	if u.Scheme != "http" || plainHTTPHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s %q would send this machine's token unencrypted; use https, or http only to localhost or a Kubernetes Service (*.svc, *.svc.cluster.local)", flag, raw)
}

func plainHTTPHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}
