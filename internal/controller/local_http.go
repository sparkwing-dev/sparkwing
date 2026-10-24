package controller

import (
	"fmt"
	"net/http"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

// HTTP supplies shared policy handlers for the elected local runtime.
func (s *Server) HTTP() localhostwire.LocalHTTP {
	s.localHTTPOnce.Do(func() {
		s.WithPeerIdentity(func(r *http.Request) capability.Subject {
			peer, ok := localhostwire.LocalPeer(r.Context())
			if !ok {
				return capability.Subject{}
			}
			switch peer.GOOS {
			case "linux", "darwin":
				if peer.UID != nil {
					return capability.Subject{Kind: capability.SubjectLocal, ID: fmt.Sprintf("local:%d", *peer.UID)}
				}
			case "windows":
				if peer.SID != "" {
					return capability.Subject{Kind: capability.SubjectLocal, ID: "local:" + peer.SID}
				}
			}
			return capability.Subject{}
		})
		router := s.rebuildRouters()
		s.registerControllerEndpoints(router, []controllerwire.Endpoint{{Method: "POST", Pattern: "/api/v1/local/doctor", Authentication: capability.AuthCapability, Decode: authorityTarget, Handle: s.localDoctor}})
		api := s.observedHandler(router.ServeMux)
		s.localHTTP = localhostwire.LocalHTTP{API: api, Dashboard: api}
		if s.dashboard != nil {
			s.localHTTP.Dashboard = s.browserHandler(api)
		}
		if s.dashboard != nil && s.dashboard.Local {
			s.localHTTP.Shell = s.localShell()
		}
	})
	return s.localHTTP
}
