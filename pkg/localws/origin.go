package localws

import (
	"net"

	"github.com/sparkwing-dev/sparkwing/internal/originguard"
)

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
	return originguard.LoopbackHost(host)
}
